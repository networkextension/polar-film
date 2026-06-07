package film

// people_store.go — people + media_people (cast/crew links).

import (
	"context"
	"time"
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
