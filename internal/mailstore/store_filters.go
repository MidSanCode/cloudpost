package mailstore

import "database/sql"

// ListFilters lists filters of an account ordered by position.
func (s *Store) ListFilters(accountID int64) ([]*Filter, error) {
	rows, err := s.DB.Query(`SELECT id,account_id,name,enabled,position,cond_field,cond_op,cond_value,action,action_arg
		FROM filters WHERE account_id=? ORDER BY position,id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Filter{}
	for rows.Next() {
		f := &Filter{}
		var enabled int64
		if err := rows.Scan(&f.ID, &f.AccountID, &f.Name, &enabled, &f.Position, &f.CondField, &f.CondOp, &f.CondValue, &f.Action, &f.ActionArg); err != nil {
			return nil, err
		}
		f.Enabled = i2bool(enabled)
		out = append(out, f)
	}
	return out, rows.Err()
}

// CreateFilter adds a filter.
func (s *Store) CreateFilter(f *Filter) (*Filter, error) {
	var pos int
	if err := s.DB.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM filters WHERE account_id=?`, f.AccountID).Scan(&pos); err != nil {
		pos = 1
	}
	if f.Position > 0 {
		pos = f.Position
	}
	res, err := s.DB.Exec(`INSERT INTO filters(account_id,name,enabled,position,cond_field,cond_op,cond_value,action,action_arg,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		f.AccountID, f.Name, bool2i(f.Enabled), pos, f.CondField, f.CondOp, f.CondValue, f.Action, f.ActionArg, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetFilter(f.AccountID, id)
}

// GetFilter loads one filter.
func (s *Store) GetFilter(accountID, id int64) (*Filter, error) {
	row := s.DB.QueryRow(`SELECT id,account_id,name,enabled,position,cond_field,cond_op,cond_value,action,action_arg
		FROM filters WHERE account_id=? AND id=?`, accountID, id)
	f := &Filter{}
	var enabled int64
	if err := row.Scan(&f.ID, &f.AccountID, &f.Name, &enabled, &f.Position, &f.CondField, &f.CondOp, &f.CondValue, &f.Action, &f.ActionArg); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	f.Enabled = i2bool(enabled)
	return f, nil
}

// UpdateFilter rewrites an existing filter.
func (s *Store) UpdateFilter(f *Filter) error {
	_, err := s.DB.Exec(`UPDATE filters SET name=?,enabled=?,position=?,cond_field=?,cond_op=?,cond_value=?,action=?,action_arg=?
		WHERE account_id=? AND id=?`,
		f.Name, bool2i(f.Enabled), f.Position, f.CondField, f.CondOp, f.CondValue, f.Action, f.ActionArg, f.AccountID, f.ID)
	return err
}

// DeleteFilter removes a filter.
func (s *Store) DeleteFilter(accountID, id int64) error {
	_, err := s.DB.Exec(`DELETE FROM filters WHERE account_id=? AND id=?`, accountID, id)
	return err
}

// MoveToFolderName moves one message into the named folder (creating it).
func (s *Store) MoveToFolderName(accountID int64, msgID int64, name string) error {
	folderID, err := s.EnsureFolder(accountID, name, "")
	if err != nil {
		return err
	}
	return s.MoveMessages(accountID, folderID, []int64{msgID})
}
