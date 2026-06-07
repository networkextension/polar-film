package film

// tags_store.go — tags + media_tags links.

import "context"

type Tag struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
}

// MovieTag is a media_tags row joined with the tag name/kind.
type MovieTag struct {
	TagID  string `json:"tag_id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

func (p *Plugin) insertTag(ctx context.Context, t Tag) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO tags (id, workspace_id, name, kind) VALUES ($1,$2,$3,$4)`,
		t.ID, t.WorkspaceID, t.Name, t.Kind)
	return err
}

// ensureTag returns the id of a tag by (workspace,name), creating it if absent.
func (p *Plugin) ensureTag(ctx context.Context, wsID, name, kind string) (string, error) {
	var id string
	err := p.DB.QueryRowContext(ctx, `SELECT id FROM tags WHERE workspace_id=$1 AND name=$2`, wsID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	id = newID("tg_")
	if kind == "" {
		kind = "genre"
	}
	if e := p.insertTag(ctx, Tag{ID: id, WorkspaceID: wsID, Name: name, Kind: kind}); e != nil {
		return "", e
	}
	return id, nil
}

func (p *Plugin) listTags(ctx context.Context, wsID string) ([]Tag, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT id, workspace_id, name, kind FROM tags WHERE workspace_id=$1 ORDER BY kind, name`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Kind); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Plugin) attachTag(ctx context.Context, wsID, mediaID, tagID, source string) error {
	if source == "" {
		source = "manual"
	}
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO media_tags (media_id, tag_id, source)
		SELECT $1,$2,$3
		WHERE EXISTS (SELECT 1 FROM media_items WHERE id=$1 AND workspace_id=$4)
		  AND EXISTS (SELECT 1 FROM tags        WHERE id=$2 AND workspace_id=$4)
		ON CONFLICT (media_id, tag_id) DO UPDATE SET source=EXCLUDED.source`,
		mediaID, tagID, source, wsID)
	return err
}

func (p *Plugin) detachTag(ctx context.Context, wsID, mediaID, tagID string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `
		DELETE FROM media_tags
		WHERE media_id=$1 AND tag_id=$2
		  AND EXISTS (SELECT 1 FROM media_items WHERE id=$1 AND workspace_id=$3)`,
		mediaID, tagID, wsID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Plugin) listMovieTags(ctx context.Context, wsID, mediaID string) ([]MovieTag, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT mt.tag_id, t.name, t.kind, mt.source
		FROM media_tags mt JOIN tags t ON t.id = mt.tag_id
		WHERE mt.media_id=$1 AND t.workspace_id=$2
		ORDER BY t.kind, t.name`, mediaID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MovieTag{}
	for rows.Next() {
		var mt MovieTag
		if err := rows.Scan(&mt.TagID, &mt.Name, &mt.Kind, &mt.Source); err != nil {
			return nil, err
		}
		out = append(out, mt)
	}
	return out, rows.Err()
}
