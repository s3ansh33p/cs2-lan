package db

type ScheduleItem struct {
	ID          int64  `json:"id"`
	StartAt     string `json:"start_at"`
	EndAt       string `json:"end_at"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

func (db *DB) ListScheduleItems() ([]ScheduleItem, error) {
	rows, err := db.Query(`SELECT id, start_at, end_at, title, description, color
		FROM schedule_items WHERE start_at != '' ORDER BY start_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ScheduleItem
	for rows.Next() {
		var s ScheduleItem
		if err := rows.Scan(&s.ID, &s.StartAt, &s.EndAt, &s.Title, &s.Description, &s.Color); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

func (db *DB) CreateScheduleItem(startAt, endAt, title, description, color string) (int64, error) {
	if color == "" {
		color = "blue"
	}
	res, err := db.Exec(`INSERT INTO schedule_items (start_at, end_at, title, description, color)
		VALUES (?, ?, ?, ?, ?)`, startAt, endAt, title, description, color)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) UpdateScheduleItem(id int64, startAt, endAt, title, description, color string) error {
	_, err := db.Exec(`UPDATE schedule_items SET start_at=?, end_at=?, title=?, description=?, color=?
		WHERE id=?`, startAt, endAt, title, description, color, id)
	return err
}

func (db *DB) DeleteScheduleItem(id int64) error {
	_, err := db.Exec(`DELETE FROM schedule_items WHERE id=?`, id)
	return err
}
