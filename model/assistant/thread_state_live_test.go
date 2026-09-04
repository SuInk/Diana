// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// TestLiveSub2APISharedGomoku exercises the real Responses API and native tool
// loop. CI skips it unless an explicit disposable credential is supplied.
func TestLiveSub2APISharedGomoku(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("DIANA_LIVE_SUB2API_KEY"))
	if apiKey == "" {
		t.Skip("DIANA_LIVE_SUB2API_KEY is not set")
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider:  llm.ProviderOpenAICompatible,
		APIKey:    apiKey,
		BaseURL:   "https://sub2api.earlyso.com/v1",
		APIStyle:  llm.APIStyleResponses,
		Model:     "gpt-5.6-terra",
		UserAgent: "diana-live-thread-state-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 4, 15, 22, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)

	runTurn := func(userID, messageID, instruction string) *agent.Response {
		t.Helper()
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "parallel-universe-gomoku", UserID: userID, MessageID: messageID}
		messages := []llm.Message{{
			Role:    llm.RoleSystem,
			Content: promptToolThreadState + "\n这是自动化验收。必须严格按用户给出的 JSON 调用工具，不得省略工具调用、改变坐标或另建 task_kind。",
		}}
		if state := runtime.privateThreadStateContext(context.Background(), event); state != "" {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: state, Priority: llm.MessagePriorityPlugin, AtomicText: true})
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: instruction})
		runner, err := agent.NewRunner(client, agent.Config{
			WorkDir:                t.TempDir(),
			MaxSteps:               8,
			ToolTimeoutMS:          30_000,
			FinalizationReserveMS:  10_000,
			EvidenceLedgerAdvisory: true,
		}, agent.NewToolRegistry(newDianaThreadStateTool(runtime, event)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = runner.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		response, err := runner.Run(ctx, agent.Request{Messages: messages, TraceID: "live-gomoku-" + messageID})
		if err != nil {
			t.Fatalf("turn %s failed: %v", messageID, err)
		}
		return response
	}

	first := runTurn("player-a", "m1", `创建共享五子棋。调用 diana.thread_state set：scope=session，task_kind=game.gomoku，state 严格为 {"board_size":15,"moves":[{"color":"black","point":"H8"}],"next":"white"}。成功后只确认已保存。`)
	requireLiveThreadStateStep(t, first, "set", ThreadStateScopeSession)
	requireLiveGomokuState(t, store, 1, []string{"H8"}, "white")

	now = now.Add(time.Minute)
	second := runTurn("player-b", "m2", `白棋落 I8。读取已注入的共享状态，然后调用 diana.thread_state set：scope=session，task_kind=game.gomoku，expected_version=1，state 严格为 {"board_size":15,"moves":[{"color":"black","point":"H8"},{"color":"white","point":"I8"}],"next":"black"}。成功后只确认已保存。`)
	requireLiveThreadStateStep(t, second, "set", ThreadStateScopeSession)
	requireLiveGomokuState(t, store, 2, []string{"H8", "I8"}, "black")

	now = now.Add(time.Minute)
	third := runTurn("player-c", "m3", `模拟并发恢复：先调用 diana.thread_state set，scope=session、task_kind=game.gomoku、expected_version=1，尝试把黑棋 G9 追加到 moves。这个调用应发生版本冲突；冲突后调用 get 读取最新状态，再用返回的版本调用 set，最终 state 必须为 {"board_size":15,"moves":[{"color":"black","point":"H8"},{"color":"white","point":"I8"},{"color":"black","point":"G9"}],"next":"white"}。完成后只确认冲突已恢复。`)
	requireLiveThreadStateStep(t, third, "get", ThreadStateScopeSession)
	requireLiveGomokuState(t, store, 3, []string{"H8", "I8", "G9"}, "white")

	now = now.Add(time.Minute)
	fourth := runTurn("player-d", "m4", `封盘。调用 diana.thread_state complete：scope=session，task_kind=game.gomoku，expected_version=3。完成后只确认已封盘。`)
	requireLiveThreadStateStep(t, fourth, "complete", ThreadStateScopeSession)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 || store.items[0].Scope != ThreadStateScopeSession || store.items[0].Status != ThreadStateCompleted || store.items[0].Version != 4 {
		t.Fatalf("final shared gomoku state = %#v", store.items)
	}
}

func TestLiveSub2APITerraVsLunaCompleteGomoku(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("DIANA_LIVE_SUB2API_KEY"))
	if apiKey == "" {
		t.Skip("DIANA_LIVE_SUB2API_KEY is not set")
	}
	newClient := func(model string) llm.LLMClient {
		t.Helper()
		client, err := llm.NewClient(llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible, APIKey: apiKey,
			BaseURL: "https://sub2api.earlyso.com/v1", APIStyle: llm.APIStyleResponses,
			Model: model, UserAgent: "diana-live-gomoku-match",
		})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	players := []struct {
		name  string
		model string
		color string
		stone string
		user  string
	}{
		{name: "Terra", model: "gpt-5.6-terra", color: "black", stone: "X", user: "model-terra"},
		{name: "Luna", model: "gpt-5.6-luna", color: "white", stone: "O", user: "model-luna"},
	}
	clients := []llm.LLMClient{newClient(players[0].model), newClient(players[1].model)}
	chooseMove := llm.ToolDefinition{
		Name: "choose_move", Description: "选择一个尚未落子的五子棋坐标",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"point": map[string]any{"type": "string", "description": "A1 到 O15 之间的坐标，例如 H8"},
			},
			"required":             []string{"point"},
			"additionalProperties": false,
		},
		Strict: true,
	}

	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 4, 16, 0, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)
	var board [15][15]string
	moves := make([]liveGomokuMove, 0, 64)
	stateVersion := 0
	var totalUsage llm.Usage

	for turn := 0; turn < 225; turn++ {
		playerIndex := turn % 2
		player := players[playerIndex]
		point, usage := liveChooseGomokuMove(t, clients[playerIndex], chooseMove, player.name, player.color, board, moves)
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.CachedInputTokens += usage.CachedInputTokens
		totalUsage.OutputTokens += usage.OutputTokens
		totalUsage.TotalTokens += usage.TotalTokens
		row, col, ok := parseLiveGomokuPoint(point)
		if !ok || board[row][col] != "" {
			t.Fatalf("%s returned unvalidated move %q", player.name, point)
		}
		board[row][col] = player.stone
		moves = append(moves, liveGomokuMove{Number: len(moves) + 1, Player: player.name, Color: player.color, Point: point})
		t.Logf("move %02d: %-5s %-5s %s", len(moves), player.name, player.color, point)

		next := players[(playerIndex+1)%2].color
		status := "active"
		winner := liveGomokuWinner(board, row, col, player.stone)
		if winner {
			status = "won"
			next = ""
		}
		stateMoves := make([]any, 0, len(moves))
		for _, move := range moves {
			stateMoves = append(stateMoves, map[string]any{"number": move.Number, "player": move.Player, "color": move.Color, "point": move.Point})
		}
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "terra-vs-luna", UserID: player.user, MessageID: fmt.Sprintf("move-%d", len(moves))}
		input := map[string]any{
			"operation": "set", "scope": "session", "task_kind": "game.gomoku",
			"state": map[string]any{
				"board_size": 15, "rules": "freestyle-five-in-a-row", "status": status,
				"players": map[string]any{"black": players[0].model, "white": players[1].model},
				"moves":   stateMoves, "next": next,
			},
			"ttl_seconds": 3600,
		}
		if stateVersion > 0 {
			input["expected_version"] = stateVersion
		}
		if _, err := newDianaThreadStateTool(runtime, event).Run(context.Background(), input); err != nil {
			t.Fatalf("persist move %d: %v", len(moves), err)
		}
		stateVersion++
		now = now.Add(time.Minute)

		if winner || len(moves) == 225 {
			endStatus := "draw"
			if winner {
				endStatus = player.name + " wins"
			}
			if _, err := newDianaThreadStateTool(runtime, event).Run(context.Background(), map[string]any{
				"operation": "complete", "scope": "session", "task_kind": "game.gomoku", "expected_version": stateVersion,
			}); err != nil {
				t.Fatalf("complete game: %v", err)
			}
			t.Logf("result: %s after %d moves\n%s", endStatus, len(moves), renderLiveGomokuBoard(board))
			t.Logf("provider usage: input=%d cached=%d output=%d total=%d", totalUsage.InputTokens, totalUsage.CachedInputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.items) != 1 || store.items[0].Status != ThreadStateCompleted || store.items[0].Version != stateVersion+1 {
				t.Fatalf("final persisted game = %#v", store.items)
			}
			return
		}
	}
	t.Fatal("gomoku loop ended without a terminal result")
}

type liveGomokuMove struct {
	Number int    `json:"number"`
	Player string `json:"player"`
	Color  string `json:"color"`
	Point  string `json:"point"`
}

func liveChooseGomokuMove(t *testing.T, client llm.LLMClient, tool llm.ToolDefinition, playerName, color string, board [15][15]string, moves []liveGomokuMove) (string, llm.Usage) {
	t.Helper()
	rejected := make([]string, 0, 3)
	for attempt := 1; attempt <= 4; attempt++ {
		prompt := fmt.Sprintf("你是五子棋选手 %s，执%s。标准 15×15 棋盘，横纵斜任意五连即胜，无禁手。X=黑，O=白，.=空。只调用 choose_move 选择一个空位，不要输出文字。\n当前棋盘：\n%s\n完整棋谱 JSON：%s", playerName, color, renderLiveGomokuBoard(board), mustLiveJSON(moves))
		if len(rejected) > 0 {
			prompt += "\n以下坐标无效或已占用，不得重复选择：" + strings.Join(rejected, ", ")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		response, err := client.Generate(ctx, llm.GenerateRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "认真对弈，优先直接获胜，其次阻挡对手的直接胜点，再考虑形成活四或双威胁。必须使用工具返回唯一落点。"},
				{Role: llm.RoleUser, Content: prompt},
			},
			Tools: []llm.ToolDefinition{tool}, ToolChoice: tool.Name, MaxOutputTokens: 128,
		})
		cancel()
		if err != nil {
			t.Fatalf("%s move attempt %d: %v", playerName, attempt, err)
		}
		for _, call := range response.ToolCalls {
			if call.Name != tool.Name {
				continue
			}
			point := strings.ToUpper(strings.TrimSpace(configToolString(call.Arguments, "point")))
			row, col, ok := parseLiveGomokuPoint(point)
			if ok && board[row][col] == "" {
				return point, response.Usage
			}
			if point != "" {
				rejected = append(rejected, point)
			}
		}
	}
	t.Fatalf("%s failed to choose a legal move after retries", playerName)
	return "", llm.Usage{}
}

func parseLiveGomokuPoint(point string) (row, col int, ok bool) {
	point = strings.ToUpper(strings.TrimSpace(point))
	if len(point) < 2 || point[0] < 'A' || point[0] > 'O' {
		return 0, 0, false
	}
	rowNumber, err := strconv.Atoi(point[1:])
	if err != nil || rowNumber < 1 || rowNumber > 15 {
		return 0, 0, false
	}
	return rowNumber - 1, int(point[0] - 'A'), true
}

func liveGomokuWinner(board [15][15]string, row, col int, stone string) bool {
	for _, direction := range [][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}} {
		count := 1
		for _, sign := range []int{-1, 1} {
			for step := 1; ; step++ {
				r := row + direction[0]*step*sign
				c := col + direction[1]*step*sign
				if r < 0 || r >= 15 || c < 0 || c >= 15 || board[r][c] != stone {
					break
				}
				count++
			}
		}
		if count >= 5 {
			return true
		}
	}
	return false
}

func renderLiveGomokuBoard(board [15][15]string) string {
	var out strings.Builder
	out.WriteString("    A B C D E F G H I J K L M N O\n")
	for row := 0; row < 15; row++ {
		fmt.Fprintf(&out, "%02d  ", row+1)
		for col := 0; col < 15; col++ {
			stone := board[row][col]
			if stone == "" {
				stone = "."
			}
			out.WriteString(stone)
			if col < 14 {
				out.WriteByte(' ')
			}
		}
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}

func mustLiveJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func requireLiveThreadStateStep(t *testing.T, response *agent.Response, operation string, scope ThreadStateScope) {
	t.Helper()
	for _, step := range response.Steps {
		if step.Tool == dianaThreadStateToolName && strings.TrimSpace(configToolString(step.Input, "operation")) == operation && strings.TrimSpace(configToolString(step.Input, "scope")) == string(scope) {
			return
		}
	}
	t.Fatalf("missing diana.thread_state %s scope=%s in steps: %#v", operation, scope, response.Steps)
}

func requireLiveGomokuState(t *testing.T, store *memoryThreadStateStore, version int, points []string, next string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 {
		t.Fatalf("parallel gomoku states = %#v", store.items)
	}
	item := store.items[0]
	if item.Scope != ThreadStateScopeSession || item.UserID != "" || item.TaskKind != "game.gomoku" || item.Status != ThreadStateActive || item.Version != version {
		t.Fatalf("gomoku metadata = %#v", item)
	}
	var state struct {
		Moves []struct {
			Point string `json:"point"`
		} `json:"moves"`
		Next string `json:"next"`
	}
	if err := json.Unmarshal(item.State, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Moves) != len(points) || state.Next != next {
		t.Fatalf("gomoku state = %s", item.State)
	}
	for index, point := range points {
		if state.Moves[index].Point != point {
			t.Fatalf("move %d = %q, want %q; state=%s", index, state.Moves[index].Point, point, item.State)
		}
	}
}
