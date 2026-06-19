package film

// people_store.go — people + media_people (cast/crew links).

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

type Person struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	Name          string    `json:"name"`
	AvatarAssetID string    `json:"avatar_asset_id,omitempty"`
	Bio           string    `json:"bio,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// CastMember is a media_people row joined with the person's name.
type CastMember struct {
	PersonID  string `json:"person_id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Character string `json:"character,omitempty"`
	Ord       int    `json:"ord"`
}

func (p *Plugin) insertPerson(ctx context.Context, ps Person) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO people (id, workspace_id, name, avatar_asset_id, bio, created_at)
		VALUES ($1,$2,$3,$4,$5, now())`,
		ps.ID, ps.WorkspaceID, ps.Name, ps.AvatarAssetID, ps.Bio)
	return err
}

// ensurePersonTx returns the id of the workspace's person with this name,
// creating it if absent. Used to resolve a subtitle's named speaker (e.g.
// "Darcy") to a stable person id; runs inside the subtitle-ingest tx.
func (p *Plugin) ensurePersonTx(ctx context.Context, tx *sql.Tx, wsID, name string) (string, error) {
	var id string
	// DO UPDATE (not DO NOTHING) so RETURNING fires on an existing row too.
	err := tx.QueryRowContext(ctx, `
		INSERT INTO people (id, workspace_id, name)
		VALUES ($1,$2,$3)
		ON CONFLICT (workspace_id, name) DO UPDATE SET name=EXCLUDED.name
		RETURNING id`,
		newID("pe_"), wsID, name).Scan(&id)
	return id, err
}

// resolveSpeakersForWorkspace backfills person_id for segments that carry a
// named speaker_key but no person yet (data ingested before P4b, or before its
// speaker was named). Returns the count of segments updated. Idempotent.
func (p *Plugin) resolveSpeakersForWorkspace(ctx context.Context, wsID string) (int, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT DISTINCT speaker_key FROM subtitle_segments
		WHERE workspace_id=$1 AND speaker_key IS NOT NULL AND person_id IS NULL`, wsID)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, err
		}
		if !isAnonymousSpeaker(k) {
			names = append(names, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, name := range names {
		tx, err := p.DB.BeginTx(ctx, nil)
		if err != nil {
			return updated, err
		}
		pid, err := p.ensurePersonTx(ctx, tx, wsID, name)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			return updated, err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE subtitle_segments SET person_id=$3
			WHERE workspace_id=$1 AND speaker_key=$2 AND person_id IS NULL`, wsID, name, pid)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			return updated, err
		}
		if err := tx.Commit(); err != nil {
			return updated, err
		}
		n, _ := res.RowsAffected()
		updated += int(n)
	}
	return updated, nil
}

// updatePerson applies a partial update (nil pointer = leave as-is). Returns
// false if no such person in the workspace. Unique (workspace,name) violations
// surface as a pq error (the handler maps to 409).
func (p *Plugin) updatePerson(ctx context.Context, wsID, id string, name, avatar, bio *string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `
		UPDATE people SET
		  name            = COALESCE($3, name),
		  avatar_asset_id = COALESCE($4, avatar_asset_id),
		  bio             = COALESCE($5, bio)
		WHERE id=$1 AND workspace_id=$2`,
		id, wsID, strPtr(name), strPtr(avatar), strPtr(bio))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// mergePeople folds the `from` people into `into`: repoint台词 segments, face
// clusters and cast links, then delete the merged rows. Transactional.
func (p *Plugin) mergePeople(ctx context.Context, wsID, into string, from []string) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM people WHERE id=$1 AND workspace_id=$2)`, into, wsID).Scan(&ok); err != nil {
		return err
	} else if !ok {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subtitle_segments SET person_id=$1 WHERE workspace_id=$2 AND person_id=ANY($3)`, into, wsID, pq.Array(from)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE face_clusters SET person_id=$1 WHERE workspace_id=$2 AND person_id=ANY($3)`, into, wsID, pq.Array(from)); err != nil {
		return err
	}
	// re-point cast links, skipping (media,person,role) rows that already exist
	// for `into`, then drop the leftover from-rows.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO media_people (media_id, person_id, role, character, ord)
		SELECT media_id, $1, role, character, ord FROM media_people WHERE person_id=ANY($2)
		ON CONFLICT (media_id, person_id, role) DO NOTHING`, into, pq.Array(from)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_people WHERE person_id=ANY($1)`, pq.Array(from)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE id=ANY($1) AND workspace_id=$2`, pq.Array(from), wsID); err != nil {
		return err
	}
	return tx.Commit()
}

// deletePerson removes a person, nulling its references first.
func (p *Plugin) deletePerson(ctx context.Context, wsID, id string) (bool, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `UPDATE face_clusters SET person_id=NULL WHERE workspace_id=$1 AND person_id=$2`, wsID, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subtitle_segments SET person_id=NULL WHERE workspace_id=$1 AND person_id=$2`, wsID, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_people WHERE person_id=$1`, id); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM people WHERE id=$1 AND workspace_id=$2`, id, wsID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// strPtr maps a partial-update pointer to a COALESCE arg (nil = keep current).
func strPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (p *Plugin) listPeople(ctx context.Context, wsID string) ([]Person, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, name, avatar_asset_id, bio, created_at
		FROM people WHERE workspace_id=$1 ORDER BY name`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var ps Person
		if err := rows.Scan(&ps.ID, &ps.WorkspaceID, &ps.Name, &ps.AvatarAssetID, &ps.Bio, &ps.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

// attachPerson links a person to a media item with a role (idempotent upsert
// on the (media,person,role) key). Verifies both belong to the workspace.
func (p *Plugin) attachPerson(ctx context.Context, wsID, mediaID, personID, role, character string, ord int) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO media_people (media_id, person_id, role, character, ord)
		SELECT $1,$2,$3,$4,$5
		WHERE EXISTS (SELECT 1 FROM media_items WHERE id=$1 AND workspace_id=$6)
		  AND EXISTS (SELECT 1 FROM people      WHERE id=$2 AND workspace_id=$6)
		ON CONFLICT (media_id, person_id, role)
		DO UPDATE SET character=EXCLUDED.character, ord=EXCLUDED.ord`,
		mediaID, personID, role, character, ord, wsID)
	return err
}

func (p *Plugin) detachPerson(ctx context.Context, wsID, mediaID, personID, role string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `
		DELETE FROM media_people
		WHERE media_id=$1 AND person_id=$2 AND role=$3
		  AND EXISTS (SELECT 1 FROM media_items WHERE id=$1 AND workspace_id=$4)`,
		mediaID, personID, role, wsID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Plugin) listMoviePeople(ctx context.Context, wsID, mediaID string) ([]CastMember, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT mp.person_id, pe.name, mp.role, mp.character, mp.ord
		FROM media_people mp JOIN people pe ON pe.id = mp.person_id
		WHERE mp.media_id=$1 AND pe.workspace_id=$2
		ORDER BY mp.ord, pe.name`, mediaID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CastMember{}
	for rows.Next() {
		var cm CastMember
		if err := rows.Scan(&cm.PersonID, &cm.Name, &cm.Role, &cm.Character, &cm.Ord); err != nil {
			return nil, err
		}
		out = append(out, cm)
	}
	return out, rows.Err()
}
