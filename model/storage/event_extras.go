package storage

import "context"

// LoadEventMemories is only called when a user expands the event trace.
func (s *SQLiteStore) LoadEventMemories(ctx context.Context, id string) (InboundEventDetail, error) {
	item := InboundEventDetail{ID: id}
	err := s.eventReader().QueryRowContext(ctx, `SELECT COALESCE(message_id,''), COALESCE(profile_id,''), COALESCE(group_id,''), COALESCE(user_id,'') FROM inbound_events WHERE id=?`, id).Scan(&item.MessageID, &item.ProfileID, &item.GroupID, &item.UserID)
	if err != nil {
		return item, err
	}
	items := []InboundEventDetail{item}
	err = s.attachInboundEventMemories(ctx, items)
	return items[0], err
}
