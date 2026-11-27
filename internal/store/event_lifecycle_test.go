package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/store"
)

func TestExternalEventLifecycle(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	st := store.New(handle)
	user, err := st.CreateUser(store.CreateUserParams{Username: "eventlife", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	guild, err := st.CreateGuild(user.ID, store.CreateGuildParams{Name: "Lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	end := at.Add(time.Hour).Format(time.RFC3339)
	event, err := st.CreateScheduledEvent(guild.ID, user.ID, store.CreateEventParams{
		Name: "External", Start: at.Format(time.RFC3339), End: &end, EntityType: 3, PrivacyLevel: 2,
		EntityMetadata: map[string]any{"location": "Town Hall"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if events, err := st.AdvanceExternalEvents(at.Add(-time.Second)); err != nil || len(events) != 0 {
		t.Fatalf("early %+v %v", events, err)
	}
	if events, err := st.AdvanceExternalEvents(at); err != nil || len(events) != 1 || events[0].Status != 2 {
		t.Fatalf("start %+v %v", events, err)
	}
	if events, err := st.AdvanceExternalEvents(at); err != nil || len(events) != 0 {
		t.Fatalf("duplicate %+v %v", events, err)
	}
	if events, err := st.AdvanceExternalEvents(at.Add(time.Hour)); err != nil || len(events) != 1 || events[0].Status != 3 {
		t.Fatalf("end %+v %v", events, err)
	}
	active := 2
	if _, err := st.UpdateScheduledEvent(guild.ID, event.ID, store.UpdateEventParams{Status: &active}); err == nil {
		t.Fatal("store revived terminal event")
	}
	if events, err := st.AdvanceExternalEvents(at.Add(2 * time.Hour)); err != nil || len(events) != 0 {
		t.Fatalf("terminal %+v %v", events, err)
	}
	users, err := st.EventUsers(event.ID, guild.ID, "", "", 100, true)
	if err != nil || len(users) != 1 || users[0].UserID != user.ID {
		t.Fatalf("creator subscription %+v %v", users, err)
	}
	if err := st.DeleteScheduledEvent(guild.ID, event.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.EventUserCount(event.ID); err != nil || n != 0 {
		t.Fatalf("subscription cascade %d %v", n, err)
	}
}
