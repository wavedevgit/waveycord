package api_test

import (
	"testing"
	"time"
)

type pollAnswerResp struct {
	AnswerID  int `json:"answer_id"`
	PollMedia struct {
		Text string `json:"text"`
	} `json:"poll_media"`
}

type pollResp struct {
	Question struct {
		Text string `json:"text"`
	} `json:"question"`
	Answers []pollAnswerResp `json:"answers"`
	Expiry  string           `json:"expiry"`
	Layout  int              `json:"layout_type"`
	Multi   bool             `json:"allow_multiselect"`
	Results *struct {
		IsFinalized  bool `json:"is_finalized"`
		AnswerCounts []struct {
			ID      int  `json:"id"`
			Count   int  `json:"count"`
			MeVoted bool `json:"me_voted"`
		} `json:"answer_counts"`
	} `json:"results"`
}

type pollMessage struct {
	ID   string    `json:"id"`
	Type int       `json:"type"`
	Poll *pollResp `json:"poll"`
}

func TestPollsCreateVoteExpireAndVoters(t *testing.T) {
	h := newHarness(t)
	alice := h.register("pollalice")
	bob := h.register("pollbob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Poll Guild"}, &guild)
	var channel struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "chat"}, &channel)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+channel.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	pollBody := map[string]any{
		"poll": map[string]any{
			"question": map[string]any{"text": "Best citrus?"},
			"answers": []any{
				map[string]any{"poll_media": map[string]any{"text": "Orange", "emoji": map[string]any{"name": "🍊"}}},
				map[string]any{"poll_media": map[string]any{"text": "Lime"}},
			},
			"duration": 1,
		},
	}
	var msg pollMessage
	if status := h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, pollBody, &msg); status != 200 {
		t.Fatalf("create poll status %d", status)
	}
	if msg.Type != 32 || msg.Poll == nil {
		t.Fatalf("poll message shape: %+v", msg)
	}
	if msg.Poll.Question.Text != "Best citrus?" || len(msg.Poll.Answers) != 2 {
		t.Fatalf("poll body: %+v", msg.Poll)
	}
	if msg.Poll.Answers[0].AnswerID != 1 || msg.Poll.Answers[1].AnswerID != 2 {
		t.Fatalf("answer ids: %+v", msg.Poll.Answers)
	}
	if msg.Poll.Layout != 1 || msg.Poll.Multi {
		t.Fatalf("defaults: %+v", msg.Poll)
	}
	if _, err := time.Parse(time.RFC3339, msg.Poll.Expiry); err != nil {
		t.Fatalf("expiry %q: %v", msg.Poll.Expiry, err)
	}
	if msg.Poll.Results == nil || msg.Poll.Results.IsFinalized || len(msg.Poll.Results.AnswerCounts) != 0 {
		t.Fatalf("initial results: %+v", msg.Poll.Results)
	}

	// One vote, then a change of vote, then a retract.
	vote := func(token string, ids []int) int {
		status, _ := h.request("PUT", "/channels/"+channel.ID+"/polls/"+msg.ID+"/answers/@me", token,
			map[string]any{"answer_ids": ids})
		return status
	}
	if status := vote(bob.Token, []int{2}); status != 204 {
		t.Fatalf("vote status %d", status)
	}
	var got pollMessage
	h.decode("GET", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil, &got)
	if got.Poll.Results == nil || len(got.Poll.Results.AnswerCounts) != 1 ||
		got.Poll.Results.AnswerCounts[0].ID != 2 || got.Poll.Results.AnswerCounts[0].Count != 1 {
		t.Fatalf("after vote: %+v", got.Poll.Results)
	}

	if status := vote(bob.Token, []int{1}); status != 204 {
		t.Fatalf("change vote status %d", status)
	}
	h.decode("GET", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil, &got)
	if len(got.Poll.Results.AnswerCounts) != 1 || got.Poll.Results.AnswerCounts[0].ID != 1 {
		t.Fatalf("single-select did not replace: %+v", got.Poll.Results.AnswerCounts)
	}

	if status := vote(bob.Token, nil); status != 204 {
		t.Fatalf("retract status %d", status)
	}
	h.decode("GET", "/channels/"+channel.ID+"/messages/"+msg.ID, alice.Token, nil, &got)
	if len(got.Poll.Results.AnswerCounts) != 0 {
		t.Fatalf("retract left counts: %+v", got.Poll.Results.AnswerCounts)
	}

	// Single-select rejects two answers.
	if status := vote(bob.Token, []int{1, 2}); status != 400 {
		t.Fatalf("multi on single-select status %d", status)
	}
	// Unknown answer id is rejected.
	if status := vote(bob.Token, []int{9}); status != 400 {
		t.Fatalf("unknown answer status %d", status)
	}

	// Voters listing.
	if status := vote(bob.Token, []int{1}); status != 204 {
		t.Fatalf("vote before voters status %d", status)
	}
	var voters struct {
		Users []struct{ ID string } `json:"users"`
	}
	if status := h.decode("GET", "/channels/"+channel.ID+"/polls/"+msg.ID+"/answers/1", alice.Token, nil, &voters); status != 200 ||
		len(voters.Users) != 1 || voters.Users[0].ID != bob.ID {
		t.Fatalf("voters %d %+v", status, voters)
	}
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/polls/"+msg.ID+"/answers/9", alice.Token, nil); status != 400 {
		t.Fatalf("voters unknown answer %d", status)
	}

	// Only the author ends the poll.
	if status, _ := h.request("POST", "/channels/"+channel.ID+"/polls/"+msg.ID+"/expire", bob.Token, nil); status != 403 {
		t.Fatalf("non-author expire status %d", status)
	}
	var ended pollMessage
	if status := h.decode("POST", "/channels/"+channel.ID+"/polls/"+msg.ID+"/expire", alice.Token, nil, &ended); status != 200 {
		t.Fatalf("expire status %d", status)
	}
	if ended.Poll == nil || ended.Poll.Results == nil || !ended.Poll.Results.IsFinalized {
		t.Fatalf("expire did not finalize: %+v", ended.Poll)
	}
	if status := vote(bob.Token, []int{1}); status != 400 {
		t.Fatalf("vote after expire status %d", status)
	}

	// Multiselect accepts both answers.
	multiBody := map[string]any{
		"poll": map[string]any{
			"question":          map[string]any{"text": "Pick any"},
			"answers":           []any{map[string]any{"poll_media": map[string]any{"text": "A"}}, map[string]any{"poll_media": map[string]any{"text": "B"}}},
			"duration":          2,
			"allow_multiselect": true,
		},
	}
	var multi pollMessage
	h.decode("POST", "/channels/"+channel.ID+"/messages", alice.Token, multiBody, &multi)
	status, _ := h.request("PUT", "/channels/"+channel.ID+"/polls/"+multi.ID+"/answers/@me", bob.Token,
		map[string]any{"answer_ids": []int{1, 2}})
	if status != 204 {
		t.Fatalf("multiselect vote status %d", status)
	}
	h.decode("GET", "/channels/"+channel.ID+"/messages/"+multi.ID, alice.Token, nil, &got)
	if len(got.Poll.Results.AnswerCounts) != 2 {
		t.Fatalf("multiselect counts: %+v", got.Poll.Results.AnswerCounts)
	}

	// Validation: missing question, eleven answers, zero duration, wrong layout.
	bad := func(body map[string]any) int {
		status, _ := h.request("POST", "/channels/"+channel.ID+"/messages", alice.Token, body)
		return status
	}
	if status := bad(map[string]any{"poll": map[string]any{"answers": []any{map[string]any{"poll_media": map[string]any{"text": "A"}}}}}); status != 400 {
		t.Fatalf("missing question status %d", status)
	}
	many := []any{}
	for i := 0; i < 11; i++ {
		many = append(many, map[string]any{"poll_media": map[string]any{"text": "x"}})
	}
	if status := bad(map[string]any{"poll": map[string]any{"question": map[string]any{"text": "q"}, "answers": many}}); status != 400 {
		t.Fatalf("eleven answers status %d", status)
	}
	if status := bad(map[string]any{"poll": map[string]any{"question": map[string]any{"text": "q"}, "answers": []any{map[string]any{"poll_media": map[string]any{"text": "a"}}}, "duration": 0}}); status != 400 {
		t.Fatalf("zero duration status %d", status)
	}
	if status := bad(map[string]any{"poll": map[string]any{"question": map[string]any{"text": "q"}, "answers": []any{map[string]any{"poll_media": map[string]any{"text": "a"}}}, "layout_type": 2}}); status != 400 {
		t.Fatalf("bad layout status %d", status)
	}

	// Deleting the message removes the poll rows with it.
	if status, _ := h.request("DELETE", "/channels/"+channel.ID+"/messages/"+multi.ID, alice.Token, nil); status != 204 {
		t.Fatalf("delete poll message status %d", status)
	}
	if status, _ := h.request("GET", "/channels/"+channel.ID+"/polls/"+multi.ID+"/answers/1", alice.Token, nil); status != 404 {
		t.Fatalf("poll survived message delete: %d", status)
	}
}
