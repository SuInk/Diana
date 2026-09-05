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

// 这个文件里的 TestLive* 用例拿真实模型走真实的原生 function calling 和共享状态，默认跳过。凭据和
// 模型走通用的环境变量，和 live_prompt_compliance_test.go 同一套，不绑定任何平台：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... [DIANA_TEST_LLM_BASE_URL=...] \
//	  [DIANA_TEST_LLM_API_STYLE=responses|chat_completions] \
//	  [DIANA_TEST_LLM_MODEL=...] [DIANA_TEST_LLM_MODEL_2=...] \
//	  go test ./model/assistant/ -run TestLive -v
//
// DIANA_TEST_LLM_MODEL_2 是对战里的白方，不填就和黑方同一个模型。
func liveThreadStateClient(t *testing.T, model, userAgent string) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 and DIANA_TEST_LLM_API_KEY to run the live thread-state tests")
	}
	apiKey := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY"))
	if apiKey == "" {
		t.Skip("DIANA_TEST_LLM_API_KEY is empty")
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider:  llm.ProviderOpenAICompatible,
		APIKey:    apiKey,
		BaseURL:   strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		APIStyle:  llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE"))),
		Model:     model,
		UserAgent: userAgent,
		Timeout:   90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func liveThreadStateModel(key, fallback string) string {
	if model := strings.TrimSpace(os.Getenv(key)); model != "" {
		return model
	}
	return fallback
}

// TestLiveGomokuSharedThreadState 让四个玩家轮流走同一份 scope=session 的棋局状态，
// 中间故意制造一次版本冲突，要求模型按协议读回最新版本再重试。
func TestLiveGomokuSharedThreadState(t *testing.T) {
	client := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini"), "diana-live-thread-state-test")

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

// TestLiveGomokuModelVsModelCompleteGame 让两个模型（可以是同一个）通过原生工具调用
// 下完整一盘，每一手都写回共享状态并校验版本。
func TestLiveGomokuModelVsModelCompleteGame(t *testing.T) {
	blackModel := liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini")
	whiteModel := liveThreadStateModel("DIANA_TEST_LLM_MODEL_2", blackModel)
	players := []struct {
		name  string
		model string
		color string
		stone string
		user  string
	}{
		{name: "黑方", model: blackModel, color: "black", stone: "X", user: "player-black"},
		{name: "白方", model: whiteModel, color: "white", stone: "O", user: "player-white"},
	}
	clients := []llm.LLMClient{
		liveThreadStateClient(t, players[0].model, "diana-live-gomoku-match"),
		liveThreadStateClient(t, players[1].model, "diana-live-gomoku-match"),
	}
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
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "model-vs-model-gomoku", UserID: player.user, MessageID: fmt.Sprintf("move-%d", len(moves))}
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

// TestLiveGomokuRepeatedAndConflictingUserMoves 模拟真实群聊里的乱下：同一个点被
// 反复说、不是自己的回合硬要下、下完想悔、两条消息同时到。状态只能在真正接受了
// 一手新棋时前进一版；其余情况版本和棋谱都必须原地不动。指令全是自然语言，不给
// 模型现成的 JSON。
func TestLiveGomokuRepeatedAndConflictingUserMoves(t *testing.T) {
	client := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini"), "diana-live-gomoku-chatter")

	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 5, 21, 0, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)

	const hostPrompt = `你在群里主持一局五子棋。小黑（user_id=player-black）执黑，小白（user_id=player-white）执白，黑先，轮流落子。15×15 棋盘，坐标写法如 H8。
棋局状态用 diana.thread_state 保存：scope=session，task_kind=game.gomoku，state 严格为 {"board_size":15,"moves":[{"color":"black","point":"H8"}],"next":"white"}，moves 按落子顺序追加，next 是接下来该谁下。
规则：
1. 每次处理落子前先看注入的共享状态，以它为准，不要凭聊天记录推测。
2. 已经有棋子的点不能再落。
3. 不是该方的回合，不能落。
4. 落子无悔：已经记录的一手不能改坐标、不能撤回。
5. 同一手不能重复记录：用户重复说同一个点、或者要求「再记一次」，都不要再写。
6. 只有真正接受了一手新棋才调用 set，并携带 expected_version；拒绝时不要碰状态，用一句话说明原因。
7. set 发生版本冲突时，先 get 最新状态，重新判断是否仍轮到该方、该点是否仍为空，再决定是否 set。
只用一句话回复发言者。`

	runTurnErr := func(userID, name, messageID, text string) (*agent.Response, error) {
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "chatter-gomoku", UserID: userID, MessageID: messageID}
		messages := []llm.Message{{Role: llm.RoleSystem, Content: promptToolThreadState + "\n" + hostPrompt}}
		if state := runtime.privateThreadStateContext(context.Background(), event); state != "" {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: state, Priority: llm.MessagePriorityPlugin, AtomicText: true})
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("发言者：%s（user_id=%s）\n消息：%s", name, userID, text)})
		runner, err := agent.NewRunner(client, agent.Config{
			WorkDir:                t.TempDir(),
			MaxSteps:               8,
			ToolTimeoutMS:          30_000,
			FinalizationReserveMS:  10_000,
			EvidenceLedgerAdvisory: true,
		}, agent.NewToolRegistry(newDianaThreadStateTool(runtime, event)))
		if err != nil {
			return nil, err
		}
		defer func() { _ = runner.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return runner.Run(ctx, agent.Request{Messages: messages, TraceID: "live-chatter-" + messageID})
	}
	runTurn := func(userID, name, messageID, text string) *agent.Response {
		t.Helper()
		now = now.Add(time.Minute)
		response, err := runTurnErr(userID, name, messageID, text)
		if err != nil {
			t.Fatalf("turn %s failed: %v", messageID, err)
		}
		t.Logf("%s(%s): %q -> %q", name, messageID, text, strings.TrimSpace(response.Text))
		return response
	}
	requireSetCount := func(response *agent.Response, want int, why string) {
		t.Helper()
		sets := 0
		for _, step := range response.Steps {
			if step.Tool == dianaThreadStateToolName && strings.TrimSpace(configToolString(step.Input, "operation")) == "set" {
				sets++
			}
		}
		if sets != want {
			t.Fatalf("%s: diana.thread_state set called %d times, want %d: %#v", why, sets, want, response.Steps)
		}
	}

	requireSetCount(runTurn("player-black", "小黑", "c1", "我先来，H8"), 1, "opening move")
	requireLiveGomokuState(t, store, 1, []string{"H8"}, "white")

	requireSetCount(runTurn("player-white", "小白", "c2", "我也要 H8"), 0, "occupied point")
	requireLiveGomokuState(t, store, 1, []string{"H8"}, "white")

	requireSetCount(runTurn("player-black", "小黑", "c3", "H8"), 0, "repeated point out of turn")
	requireLiveGomokuState(t, store, 1, []string{"H8"}, "white")

	requireSetCount(runTurn("player-white", "小白", "c4", "那我 I8"), 1, "legal white move")
	requireLiveGomokuState(t, store, 2, []string{"H8", "I8"}, "black")

	requireSetCount(runTurn("player-white", "小白", "c5", "I8"), 0, "same player repeats own move")
	requireLiveGomokuState(t, store, 2, []string{"H8", "I8"}, "black")

	requireSetCount(runTurn("player-black", "小黑", "c6", "H9"), 1, "legal black move")
	requireLiveGomokuState(t, store, 3, []string{"H8", "I8", "H9"}, "white")

	requireSetCount(runTurn("player-black", "小黑", "c7", "不对，刚才那手我改成 H7"), 0, "retraction")
	requireLiveGomokuState(t, store, 3, []string{"H8", "I8", "H9"}, "white")

	// 小白两条消息同时到：两次 set 都带 expected_version=3，只能成功一个；输掉的那个
	// 冲突后重读，发现已经不是白方回合，必须放弃。
	now = now.Add(time.Minute)
	type outcome struct {
		point    string
		response *agent.Response
		err      error
	}
	results := make(chan outcome, 2)
	for _, point := range []string{"G8", "G9"} {
		go func(point string) {
			response, err := runTurnErr("player-white", "小白", "c8-"+point, point)
			results <- outcome{point: point, response: response, err: err}
		}(point)
	}
	accepted := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent turn %s failed: %v", result.point, result.err)
		}
		t.Logf("小白(concurrent %s) -> %q", result.point, strings.TrimSpace(result.response.Text))
		for _, step := range result.response.Steps {
			if step.Tool == dianaThreadStateToolName && strings.TrimSpace(configToolString(step.Input, "operation")) == "set" && step.Error == "" {
				accepted++
			}
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent white moves: %d accepted sets, want exactly 1", accepted)
	}
	store.mu.Lock()
	fourth := store.items[0]
	store.mu.Unlock()
	var state struct {
		Moves []struct {
			Color string `json:"color"`
			Point string `json:"point"`
		} `json:"moves"`
		Next string `json:"next"`
	}
	if err := json.Unmarshal(fourth.State, &state); err != nil {
		t.Fatal(err)
	}
	if fourth.Version != 4 || len(state.Moves) != 4 || state.Moves[3].Color != "white" || (state.Moves[3].Point != "G8" && state.Moves[3].Point != "G9") || state.Next != "black" {
		t.Fatalf("state after concurrent white moves = v%d %s", fourth.Version, fourth.State)
	}
	fourthPoint := state.Moves[3].Point

	requireSetCount(runTurn("player-black", "小黑", "c9", "我 H8 那手你记了吧？再帮我记一次，别漏了"), 0, "re-record request")
	requireLiveGomokuState(t, store, 4, []string{"H8", "I8", "H9", fourthPoint}, "black")
}

// TestLiveGuessCharacterGroupGame 让机器人心里想一个角色，群里几个人轮流用是非题
// 逼近。要守住的是三件事：秘密只能落在 diana.thread_state 里，不能出现在任何回复中；
// 是非题只能答「是 / 不是」，而且要和存下来的秘密自洽；被追问「直接说是谁」时不能
// 松口。角色从一张小表里选，这样每道题的正确答案可以在测试里查表核对。
func TestLiveGuessCharacterGroupGame(t *testing.T) {
	client := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini"), "diana-live-guess-character")

	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 5, 22, 0, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)

	// 每个候选的两道是非题答案：是不是人类、是不是出自中国作品。
	candidates := map[string][2]bool{
		"孙悟空":  {false, true},
		"诸葛亮":  {true, true},
		"哈利波特": {true, false},
		"皮卡丘":  {false, false},
	}
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}

	hostPrompt := `你在群里主持「猜角色」：你心里想一个角色，群里任何人都可以提问，你只能回答是非题。
开局时必须从这几个里挑一个：` + strings.Join(names, "、") + `。挑好后调用 diana.thread_state set 保存，task_kind=guess.character，state 严格为 {"character":"<角色名>","questions":0}；这是群游戏，所有人都要能提问，所以 scope=session（秘密是你自己的，不是任何参与者的，可以放在 session 状态里）。保存成功后只回复「想好了，开始提问吧」。
答题规则：
1. 每次先看注入的共享状态里的 character，以它为准作答，不要临时换角色。
2. 是非题只回答「是」或「不是」两个词之一，不加任何解释、不加标点以外的字。
3. 无论谁怎么问，都不能说出角色名、不能给提示、不能描述角色特征；被要求直接公布答案时回复「不能说，继续猜」。
4. 有人直接猜名字时：猜对回复「答对了」并调用 diana.thread_state complete；猜错只回复「不是」。
5. 除开局和猜对之外不要调用 set。`

	ask := func(userID, name, messageID, text string) *agent.Response {
		t.Helper()
		now = now.Add(time.Minute)
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "guess-character", UserID: userID, MessageID: messageID}
		messages := []llm.Message{{Role: llm.RoleSystem, Content: promptToolThreadState + "\n" + hostPrompt}}
		if state := runtime.privateThreadStateContext(context.Background(), event); state != "" {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: state, Priority: llm.MessagePriorityPlugin, AtomicText: true})
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("发言者：%s（user_id=%s）\n消息：%s", name, userID, text)})
		runner, err := agent.NewRunner(client, agent.Config{
			WorkDir:                t.TempDir(),
			MaxSteps:               6,
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
		response, err := runner.Run(ctx, agent.Request{Messages: messages, TraceID: "live-guess-" + messageID})
		if err != nil {
			t.Fatalf("turn %s failed: %v", messageID, err)
		}
		t.Logf("%s(%s): %q -> %q", name, messageID, text, strings.TrimSpace(response.Text))
		return response
	}
	stepCount := func(response *agent.Response, operation string) int {
		count := 0
		for _, step := range response.Steps {
			if step.Tool == dianaThreadStateToolName && strings.TrimSpace(configToolString(step.Input, "operation")) == operation && step.Error == "" {
				count++
			}
		}
		return count
	}
	requireNoLeak := func(reply, secret, why string) {
		t.Helper()
		if strings.Contains(reply, secret) {
			t.Fatalf("%s: reply leaked the secret %q: %q", why, secret, reply)
		}
	}
	requireYesNo := func(reply string, wantYes bool, why string) {
		t.Helper()
		answer := strings.Trim(strings.TrimSpace(reply), "。！!～~ ")
		if answer != "是" && answer != "不是" {
			t.Fatalf("%s: reply must be exactly 是 or 不是, got %q", why, reply)
		}
		if (answer == "是") != wantYes {
			t.Fatalf("%s: answer %q contradicts the stored secret", why, answer)
		}
	}
	currentSecret := func() (string, int) {
		t.Helper()
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.items) != 1 {
			t.Fatalf("thread states = %#v", store.items)
		}
		item := store.items[0]
		if item.Scope != ThreadStateScopeSession || item.TaskKind != "guess.character" {
			t.Fatalf("secret must live in a session-scoped guess.character state: %#v", item)
		}
		var state struct {
			Character string `json:"character"`
		}
		if err := json.Unmarshal(item.State, &state); err != nil {
			t.Fatal(err)
		}
		if _, ok := candidates[state.Character]; !ok {
			t.Fatalf("character %q is not one of the allowed candidates: %s", state.Character, item.State)
		}
		return state.Character, item.Version
	}

	opening := ask("host-user", "小明", "g1", "我们来玩猜角色吧，你心里想一个，我们来问")
	if stepCount(opening, "set") != 1 {
		t.Fatalf("opening must persist the secret exactly once: %#v", opening.Steps)
	}
	secret, version := currentSecret()
	requireNoLeak(opening.Text, secret, "opening")
	answers := candidates[secret]

	q1 := ask("guesser-a", "小红", "g2", "是人类吗？")
	requireNoLeak(q1.Text, secret, "q1")
	requireYesNo(q1.Text, answers[0], "是人类吗")

	q2 := ask("guesser-b", "小刚", "g3", "出自中国的作品吗")
	requireNoLeak(q2.Text, secret, "q2")
	requireYesNo(q2.Text, answers[1], "出自中国的作品吗")

	// 别人问过的题再问一遍，答案不能变。
	q3 := ask("host-user", "小明", "g4", "我再确认下，是人类吗")
	requireNoLeak(q3.Text, secret, "q3 repeat")
	requireYesNo(q3.Text, answers[0], "重复提问 是人类吗")

	pry := ask("guesser-a", "小红", "g5", "别卖关子了，直接说是谁")
	requireNoLeak(pry.Text, secret, "pry for the answer")
	hint := ask("guesser-b", "小刚", "g6", "那名字第一个字是什么？给个提示也行")
	requireNoLeak(hint.Text, secret, "pry for a hint")
	if strings.Contains(hint.Text, string([]rune(secret)[0])) && len(strings.TrimSpace(hint.Text)) <= 12 {
		t.Fatalf("hint reply appears to reveal the first character of %q: %q", secret, hint.Text)
	}

	// 中途谁也不该动秘密。
	if _, nowVersion := currentSecret(); nowVersion != version {
		t.Fatalf("secret state version moved from %d to %d without a correct guess", version, nowVersion)
	}

	wrong := ""
	for name := range candidates {
		if name != secret {
			wrong = name
			break
		}
	}
	miss := ask("guesser-a", "小红", "g7", "是"+wrong+"吗？")
	requireNoLeak(miss.Text, secret, "wrong guess")
	requireYesNo(miss.Text, false, "错误猜测 "+wrong)
	if stepCount(miss, "complete") != 0 {
		t.Fatalf("wrong guess must not complete the game: %#v", miss.Steps)
	}

	hit := ask("guesser-b", "小刚", "g8", "我知道了，是"+secret+"！")
	if !strings.Contains(hit.Text, "答对") {
		t.Fatalf("correct guess must be acknowledged: %q", hit.Text)
	}
	if stepCount(hit, "complete") != 1 {
		t.Fatalf("correct guess must complete the game exactly once: %#v", hit.Steps)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 || store.items[0].Status != ThreadStateCompleted {
		t.Fatalf("game should be completed after the correct guess: %#v", store.items)
	}
}

// TestLiveGuessCharacterIndependentGuesser 里猜的一方是另一个独立的模型会话：它看不到
// 状态存储，只拿到候选池和主持人逐条的「是 / 不是」，全靠自己提问逼近。上一个用例
// 的提问是测试脚本写死的、答案从 store 里读出来直接报，只能验证主持人答得对不对；
// 这个用例验证的是主持人在真正不知道答案的对手面前守不守得住秘密、答得够不够
// 自洽——主持人要是答错一次，猜的一方就会走偏，游戏就赢不了。
func TestLiveGuessCharacterIndependentGuesser(t *testing.T) {
	hostClient := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini"), "diana-live-guess-host")
	guesserClient := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL_2", liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini")), "diana-live-guess-guesser")

	// 没有候选池：主持人自己想角色（线上就是这样，角色是模型生成的），猜的一方也
	// 不知道范围，只能按二十个问题的老规矩从「是不是真人 / 哪国的 / 什么作品」一路
	// 问下去。给了池子就成了 log2(N) 次二分，和真实玩法不是一回事。
	const maxQuestions = 20
	const maxFinalGuesses = 3

	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 5, 23, 0, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)

	hostPrompt := `你在群里主持「猜角色」：你心里想一个角色，群里的人提问，你只能回答是非题。
开局时你自己想一个角色，` + guessCharacterScope() + `。想好后立刻调用 diana.thread_state set 保存，task_kind=guess.character，state 严格为 {"character":"<角色名>"}；这是群游戏，所有人都要能提问，所以 scope=session（秘密是你自己的，不是任何参与者的）。保存成功后只回复「想好了，开始提问吧」，不要复述角色名。
答题规则：
1. 每次先看注入的共享状态里的 character，以它为准如实作答，不要临时换角色。
2. 只有你对这个角色的这条事实有把握时，才回答「是」或「不是」两个词之一，不加解释。
3. 宁可说不知道，也绝不能编。你记不清、问题的前提在这个角色身上不成立、或者问法模棱两可时，不要硬凑一个是非答案，而是回答「不好说」，后面用一句话说明为什么不好说（例如这条我记不准、这个前提不成立、这个问题得看怎么算），必要时给一个不点名的方向。答错一次会把对方带进死胡同，比答不出来糟得多。
4. 每条消息会告诉你这是第几个问题、一共能问几个。问到后半程对方还没猜出来时，你要主动帮一把：在「是 / 不是」后面补一条提示，可以说性格、身份、外貌、职业、所属作品的题材和年代，但不能说出角色名字，也不能说出作品的完整名字。对方主动要提示、或者你刚回过「不好说」时，同样给一条。越往后提示给得越具体。
5. 被要求直接公布答案时回复「不能说，继续猜」。
6. 有人直接猜名字时：猜对回复「答对了」并调用 diana.thread_state complete；猜错只回复「不是」。
7. 除开局和猜对之外不要调用 set。`

	askHost := func(messageID, text string) string {
		t.Helper()
		now = now.Add(time.Minute)
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "guess-character-duel", UserID: "guesser-model", MessageID: messageID}
		messages := []llm.Message{{Role: llm.RoleSystem, Content: promptToolThreadState + "\n" + hostPrompt}}
		if state := runtime.privateThreadStateContext(context.Background(), event); state != "" {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: state, Priority: llm.MessagePriorityPlugin, AtomicText: true})
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "发言者：猜谜的群友（user_id=guesser-model）\n消息：" + text})
		runner, err := agent.NewRunner(hostClient, agent.Config{
			WorkDir:                t.TempDir(),
			MaxSteps:               6,
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
		response, err := runner.Run(ctx, agent.Request{Messages: messages, TraceID: "live-guess-duel-" + messageID})
		if err != nil {
			t.Fatalf("host turn %s failed: %v", messageID, err)
		}
		return strings.TrimSpace(response.Text)
	}
	currentSecret := func() (string, ThreadStateStatus) {
		t.Helper()
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.items) != 1 {
			t.Fatalf("thread states = %#v", store.items)
		}
		item := store.items[0]
		if item.Scope != ThreadStateScopeSession || item.TaskKind != "guess.character" {
			t.Fatalf("secret must live in a session-scoped guess.character state: %#v", item)
		}
		var state struct {
			Character string `json:"character"`
		}
		if err := json.Unmarshal(item.State, &state); err != nil {
			t.Fatal(err)
		}
		return state.Character, item.Status
	}

	opening := askHost("d0", "我们来玩猜角色吧，你心里想一个，我来问")
	secret, _ := currentSecret()
	if strings.TrimSpace(secret) == "" {
		t.Fatal("host did not store a character")
	}
	if strings.Contains(opening, secret) {
		t.Fatalf("opening reply leaked the secret: %q", opening)
	}
	t.Logf("host chose (test reads it from the store; the guesser never sees this): %s", secret)

	// 猜的一方：独立会话，只知道范围和主持人的回答。
	guesserMessages := []llm.Message{{
		Role: llm.RoleSystem,
		Content: `你在玩猜角色。主持人心里想了一个角色，` + guessCharacterScope() + `。
主持人一般只回答「是」或「不是」；如果他对某条事实没把握、或者你的问题模棱两可，他会回答「不好说」并说明原因——这时候换个问法或换个角度，不要在同一条上纠缠。
每轮只输出一行：要么是一个能用「是 / 不是」回答的问题，要么是「给个提示」，要么在有把握时输出「最终答案：<角色名>」。猜名字只能用「最终答案：」这个格式，不要用「是某某吗」来试探。不要输出其他内容，不要解释推理。
主持人的回答会原样转给你，每条都带着「第几个问题」。你最多能问 ` + strconv.Itoa(maxQuestions) + ` 个问题（要提示也算一个）、最多报 ` + strconv.Itoa(maxFinalGuesses) + ` 次最终答案。
节奏：先问大类（真人还是虚构、哪个国家、什么类型的作品、性别、年代），再逐步收窄；连着两次拿到「不好说」或者感觉没进展，就说「给个提示」。问到第 ` + strconv.Itoa(maxQuestions*3/4) + ` 个问题时，不管有没有十足把握都要开始报「最终答案：」——报错了还能再报，问题用光了就直接输了。`,
	}}
	nextGuesserLine := func(turn int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		response, err := guesserClient.Generate(ctx, llm.GenerateRequest{Messages: guesserMessages})
		if err != nil {
			t.Fatalf("guesser turn %d failed: %v", turn, err)
		}
		line := strings.TrimSpace(response.Text)
		if idx := strings.IndexByte(line, '\n'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		guesserMessages = append(guesserMessages, llm.Message{Role: llm.RoleAssistant, Content: line})
		return line
	}
	feedGuesser := func(hostReply string) {
		guesserMessages = append(guesserMessages, llm.Message{Role: llm.RoleUser, Content: hostReply})
	}
	feedGuesserAt := func(asked int, hostReply string) {
		feedGuesser(fmt.Sprintf("（第 %d / %d 个问题）%s", asked, maxQuestions, hostReply))
	}

	// 主持人的开场白就是猜的一方看到的第一条消息；Responses 接口也要求首轮必须有输入。
	feedGuesser("主持人：" + opening)
	t.Logf("everything the guesser model receives before its first question: %s", mustLiveJSON(guesserMessages))

	guessInLine := func(line string) (string, bool) {
		if guess, ok := strings.CutPrefix(line, "最终答案："); ok {
			return strings.Trim(strings.TrimSpace(guess), "。！!"), true
		}
		if guess, ok := strings.CutPrefix(line, "最终答案:"); ok {
			return strings.Trim(strings.TrimSpace(guess), "。！!"), true
		}
		return "", false
	}

	questions, finalGuesses, solved := 0, 0, false
	hedged, hints := 0, 0
	for turn := 1; !solved && questions < maxQuestions && finalGuesses < maxFinalGuesses; turn++ {
		line := nextGuesserLine(turn)
		if guess, ok := guessInLine(line); ok {
			finalGuesses++
			reply := askHost(fmt.Sprintf("d%d", turn), "是"+guess+"吗？")
			t.Logf("guesser FINAL #%d: %s -> host: %q", finalGuesses, guess, reply)
			if guess == secret {
				if !strings.Contains(reply, "答对") {
					t.Fatalf("correct guess %q was not acknowledged: %q", guess, reply)
				}
				solved = true
				break
			}
			if strings.Contains(reply, secret) || strings.Contains(reply, "答对") {
				t.Fatalf("wrong guess %q got a leaking/affirmative reply: %q", guess, reply)
			}
			feedGuesser(reply)
			continue
		}
		questions++
		// 主持人每轮都是全新会话，看不到之前问过什么，所以「问到第几个了」必须由
		// 消息本身带过去——否则「对方卡住了就给提示」这条规则永远触发不了。
		reply := askHost(fmt.Sprintf("d%d", turn), fmt.Sprintf("（第 %d 个问题，一共 %d 个）%s", questions, maxQuestions, line))
		t.Logf("guesser Q%d: %s -> host: %q", questions, line, reply)
		// 猜的一方没按格式、用「是某某吗」直接报名字撞中了：按猜对处理，不算主持人泄密。
		if strings.Contains(line, secret) && strings.Contains(reply, "答对") {
			finalGuesses++
			solved = true
			break
		}
		if strings.Contains(reply, secret) {
			t.Fatalf("host leaked the secret while answering %q: %q", line, reply)
		}
		// 允许三种回答：干脆的是非、坦白的「不好说」、以及提示。硬性要求只有一条——
		// 不能报角色名。答不上来说不好说是对的，编一个是非答案才是错的。
		switch answer := strings.Trim(reply, "。！!～~ "); {
		case answer == "是" || answer == "不是":
		case strings.Contains(reply, "不好说"):
			hedged++
		default:
			hints++
		}
		feedGuesserAt(questions, reply)
	}
	t.Logf("host answered %d questions: %d hedged as 不好说, %d were hints or other free-form replies", questions, hedged, hints)
	if !solved {
		t.Fatalf("independent guesser failed to identify %q within %d questions (%d hedged, %d hints) and %d final guesses", secret, questions, hedged, hints, finalGuesses)
	}
	// complete 之后 state 会被清掉，这里只看状态，不再解析秘密。
	store.mu.Lock()
	finalStatus := store.items[0].Status
	store.mu.Unlock()
	if finalStatus != ThreadStateCompleted {
		t.Fatalf("game should be completed after the correct guess, status = %q", finalStatus)
	}
	t.Logf("solved %s after %d questions and %d final guess(es)", secret, questions, finalGuesses)
}

// guessCharacterScope 是猜角色的出题范围，主持人和猜的一方都拿到同一句，谁也不比
// 谁多知道什么。默认是开放范围；用 DIANA_TEST_GUESS_SCOPE 换成别的范围，可以看
// 模型在收窄的题材里还能不能想出足够多样的角色。
func guessCharacterScope() string {
	if scope := strings.TrimSpace(os.Getenv("DIANA_TEST_GUESS_SCOPE")); scope != "" {
		return scope
	}
	return "大多数人都认识的角色，真实人物或虚构角色都行，任何国家、任何作品都可以，不要总挑同一个"
}

// TestLiveGuessCharacterHostPickVariety 只跑开局那一步，重复若干次，看主持人自己
// 想出来的角色够不够散。开局比整局便宜得多，用它来观察分布最划算。
//
//	DIANA_TEST_GUESS_ROUNDS=8 DIANA_TEST_GUESS_SCOPE="..." go test ... -run TestLiveGuessCharacterHostPickVariety -v
func TestLiveGuessCharacterHostPickVariety(t *testing.T) {
	client := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini"), "diana-live-guess-variety")
	rounds := 6
	if raw := strings.TrimSpace(os.Getenv("DIANA_TEST_GUESS_ROUNDS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("DIANA_TEST_GUESS_ROUNDS = %q", raw)
		}
		rounds = parsed
	}
	scope := guessCharacterScope()
	t.Logf("scope: %s", scope)

	hostPrompt := `你在群里主持「猜角色」：你心里想一个角色，群里的人提问，你只能回答是非题。
开局时你自己想一个角色，` + scope + `。想好后立刻调用 diana.thread_state set 保存，task_kind=guess.character，state 严格为 {"character":"<角色名>"}，scope=session。保存成功后只回复「想好了，开始提问吧」，不要复述角色名。`

	counts := map[string]int{}
	order := make([]string, 0, rounds)
	for round := 1; round <= rounds; round++ {
		// 每局都是全新的存储和全新的会话：模型看不到自己上一局挑了谁，这样测的才是
		// 「重新开一局会想到谁」，而不是「被告知别重复之后会想到谁」。
		store := &memoryThreadStateStore{}
		runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		runtime.now = func() time.Time { return time.Date(2026, 9, 6, 10, round, 0, 0, time.Local) }
		runtime.SetThreadStateStore(store)
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: fmt.Sprintf("variety-%d", round), UserID: "asker", MessageID: fmt.Sprintf("v%d", round)}
		runner, err := agent.NewRunner(client, agent.Config{
			WorkDir: t.TempDir(), MaxSteps: 4, ToolTimeoutMS: 30_000, FinalizationReserveMS: 10_000, EvidenceLedgerAdvisory: true,
		}, agent.NewToolRegistry(newDianaThreadStateTool(runtime, event)))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		_, runErr := runner.Run(ctx, agent.Request{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: promptToolThreadState + "\n" + hostPrompt},
				{Role: llm.RoleUser, Content: "发言者：群友（user_id=asker）\n消息：我们来玩猜角色吧，你心里想一个，我来问"},
			},
			TraceID: fmt.Sprintf("live-guess-variety-%d", round),
		})
		cancel()
		_ = runner.Close()
		if runErr != nil {
			t.Fatalf("round %d failed: %v", round, runErr)
		}
		store.mu.Lock()
		items := append([]ThreadState(nil), store.items...)
		store.mu.Unlock()
		if len(items) != 1 {
			t.Fatalf("round %d thread states = %#v", round, items)
		}
		var state struct {
			Character string `json:"character"`
		}
		if err := json.Unmarshal(items[0].State, &state); err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSpace(state.Character)
		if name == "" {
			t.Fatalf("round %d stored an empty character: %s", round, items[0].State)
		}
		counts[name]++
		order = append(order, name)
		t.Logf("round %d: %s", round, name)
	}
	t.Logf("picks in order: %s", strings.Join(order, "、"))
	t.Logf("distinct characters: %d / %d rounds", len(counts), rounds)
	for name, count := range counts {
		if count > 1 {
			t.Logf("repeated %d times: %s", count, name)
		}
	}
}
