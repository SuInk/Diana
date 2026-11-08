// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"

	"github.com/SuInk/diana/model/assistant"
)

// 本地群存在 app_state：群列表一个键，每个群的消息各一个键（上限 300 条，见
// localRoomLogCap），量很小，整份覆盖写。
const (
	localRoomsKey              = "local_rooms"
	localRoomMessagesKeyPrefix = "local_room_messages:"
)

func (s *SQLiteStore) SaveLocalRooms(ctx context.Context, rooms []assistant.LocalRoom) error {
	if rooms == nil {
		rooms = []assistant.LocalRoom{}
	}
	return s.saveJSON(ctx, localRoomsKey, rooms)
}

func (s *SQLiteStore) LoadLocalRooms(ctx context.Context) ([]assistant.LocalRoom, error) {
	var rooms []assistant.LocalRoom
	if _, err := s.loadJSON(ctx, localRoomsKey, &rooms); err != nil {
		return nil, err
	}
	return rooms, nil
}

func (s *SQLiteStore) SaveLocalRoomMessages(ctx context.Context, roomID string, messages []assistant.LocalRoomMessage) error {
	if messages == nil {
		messages = []assistant.LocalRoomMessage{}
	}
	return s.saveJSON(ctx, localRoomMessagesKeyPrefix+roomID, messages)
}

func (s *SQLiteStore) LoadLocalRoomMessages(ctx context.Context, roomID string) ([]assistant.LocalRoomMessage, error) {
	var messages []assistant.LocalRoomMessage
	if _, err := s.loadJSON(ctx, localRoomMessagesKeyPrefix+roomID, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *SQLiteStore) DeleteLocalRoomMessages(ctx context.Context, roomID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_state WHERE key = ?`, localRoomMessagesKeyPrefix+roomID)
	return err
}

var _ assistant.LocalRoomStore = (*SQLiteStore)(nil)
