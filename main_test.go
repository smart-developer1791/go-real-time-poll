package main

import (
	"sync"
	"testing"
	"time"
)

// TestGenerateID verifies that generated IDs
// have the correct length and are unique.
func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()

	// ID should be 8 hex characters (4 bytes)
	if len(id1) != 8 {
		t.Errorf("Expected ID length 8, got %d", len(id1))
	}

	// Two generated IDs should not be equal
	if id1 == id2 {
		t.Error("Expected unique IDs")
	}
}

// TestStoreCreate checks that creating a poll
// assigns an ID and creation timestamp.
func TestStoreCreate(t *testing.T) {
	store := NewStore()
	poll := &Poll{
		Question: "Test?",
		Options:  []Option{{ID: "1", Text: "A"}, {ID: "2", Text: "B"}},
	}

	store.Create(poll)

	// Poll ID must be generated
	if poll.ID == "" {
		t.Error("Expected poll ID")
	}

	// CreatedAt timestamp must be set
	if poll.CreatedAt.IsZero() {
		t.Error("Expected CreatedAt")
	}
}

// TestStoreGet verifies retrieving existing polls
// and handling non-existent poll IDs.
func TestStoreGet(t *testing.T) {
	store := NewStore()
	poll := &Poll{Question: "Test?", Options: []Option{{ID: "1", Text: "A"}}}
	store.Create(poll)

	// Existing poll should be returned
	got, exists := store.Get(poll.ID)
	if !exists {
		t.Fatal("Expected poll to exist")
	}
	if got.Question != poll.Question {
		t.Errorf("Expected %s, got %s", poll.Question, got.Question)
	}

	// Non-existent poll should not be found
	_, exists = store.Get("nonexistent")
	if exists {
		t.Error("Expected poll to not exist")
	}
}

// TestStoreVote ensures that voting increments
// the vote counter for a valid option.
func TestStoreVote(t *testing.T) {
	store := NewStore()
	poll := &Poll{
		Question: "Test?",
		Options:  []Option{{ID: "opt1", Text: "A", Votes: 0}},
	}
	store.Create(poll)

	updated, err := store.Vote(poll.ID, "opt1")
	if err != nil {
		t.Fatalf("Vote failed: %v", err)
	}

	// Vote count should be incremented
	if updated.Options[0].Votes != 1 {
		t.Errorf("Expected 1 vote, got %d", updated.Options[0].Votes)
	}
}

// TestStoreVoteInvalid checks error handling
// for invalid option IDs and poll IDs.
func TestStoreVoteInvalid(t *testing.T) {
	store := NewStore()
	poll := &Poll{Question: "Test?", Options: []Option{{ID: "opt1", Text: "A"}}}
	store.Create(poll)

	// Voting for a non-existent option should fail
	_, err := store.Vote(poll.ID, "invalid")
	if err == nil {
		t.Error("Expected error for invalid option")
	}

	// Voting for a non-existent poll should fail
	_, err = store.Vote("nonexistent", "opt1")
	if err == nil {
		t.Error("Expected error for nonexistent poll")
	}
}

// TestStoreVoteExpiredPoll ensures voting
// is rejected for expired polls.
func TestStoreVoteExpiredPoll(t *testing.T) {
	store := NewStore()
	poll := &Poll{
		Question:  "Test?",
		Options:   []Option{{ID: "opt1", Text: "A"}},
		ExpiresAt: time.Now().Add(-time.Hour), // Expired
	}
	store.Create(poll)

	_, err := store.Vote(poll.ID, "opt1")
	if err == nil {
		t.Error("Expected error for expired poll")
	}
}

// TestStoreList verifies that polls are returned
// in reverse chronological order (newest first).
func TestStoreList(t *testing.T) {
	store := NewStore()

	store.Create(&Poll{Question: "First"})
	time.Sleep(10 * time.Millisecond)
	store.Create(&Poll{Question: "Second"})
	time.Sleep(10 * time.Millisecond)
	store.Create(&Poll{Question: "Third"})

	polls := store.List()

	if len(polls) != 3 {
		t.Fatalf("Expected 3 polls, got %d", len(polls))
	}

	// Should be sorted newest first
	if polls[0].Question != "Third" {
		t.Errorf("Expected 'Third' first, got '%s'", polls[0].Question)
	}
}

// TestStoreDelete ensures polls can be deleted
// and that deleting non-existent polls fails gracefully.
func TestStoreDelete(t *testing.T) {
	store := NewStore()
	poll := &Poll{Question: "To delete"}
	store.Create(poll)

	if !store.Delete(poll.ID) {
		t.Error("Expected successful deletion")
	}

	// Poll should no longer exist
	_, exists := store.Get(poll.ID)
	if exists {
		t.Error("Poll should not exist after deletion")
	}

	// Deleting a non-existent poll should return false
	if store.Delete("nonexistent") {
		t.Error("Should not delete nonexistent poll")
	}
}

// TestPollTotalVotes verifies correct aggregation
// of votes across all options.
func TestPollTotalVotes(t *testing.T) {
	poll := &Poll{Options: []Option{{Votes: 10}, {Votes: 20}, {Votes: 30}}}

	if poll.TotalVotes() != 60 {
		t.Errorf("Expected 60, got %d", poll.TotalVotes())
	}

	// Empty poll should have zero total votes
	empty := &Poll{}
	if empty.TotalVotes() != 0 {
		t.Errorf("Expected 0, got %d", empty.TotalVotes())
	}
}

// TestPollVotePercentage verifies percentage
// calculations for options.
func TestPollVotePercentage(t *testing.T) {
	poll := &Poll{Options: []Option{
		{ID: "a", Votes: 25},
		{ID: "b", Votes: 75},
	}}

	if poll.VotePercentage("a") != 25.0 {
		t.Errorf("Expected 25%%, got %.2f%%", poll.VotePercentage("a"))
	}

	if poll.VotePercentage("b") != 75.0 {
		t.Errorf("Expected 75%%, got %.2f%%", poll.VotePercentage("b"))
	}

	// Non-existent option should return 0%
	if poll.VotePercentage("c") != 0 {
		t.Error("Expected 0 for nonexistent option")
	}

	// Poll with zero total votes should return 0%
	empty := &Poll{Options: []Option{{ID: "x", Votes: 0}}}
	if empty.VotePercentage("x") != 0 {
		t.Error("Expected 0 for poll with no votes")
	}
}

// TestPollIsExpired validates poll expiration logic.
func TestPollIsExpired(t *testing.T) {
	tests := []struct {
		name     string
		poll     *Poll
		expected bool
	}{
		{"active", &Poll{ExpiresAt: time.Now().Add(time.Hour)}, false},
		{"expired", &Poll{ExpiresAt: time.Now().Add(-time.Hour)}, true},
		{"no expiry", &Poll{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.poll.IsExpired() != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, tt.poll.IsExpired())
			}
		})
	}
}

// TestConcurrentVotes ensures that concurrent voting
// is handled correctly and safely.
func TestConcurrentVotes(t *testing.T) {
	store := NewStore()
	poll := &Poll{
		Question: "Concurrent test",
		Options:  []Option{{ID: "opt1", Text: "A", Votes: 0}},
	}
	store.Create(poll)

	var wg sync.WaitGroup
	numVotes := 100

	// Perform concurrent votes
	for i := 0; i < numVotes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Vote(poll.ID, "opt1")
		}()
	}
	wg.Wait()

	// Final vote count should equal number of goroutines
	final, _ := store.Get(poll.ID)
	if final.Options[0].Votes != numVotes {
		t.Errorf("Expected %d votes, got %d", numVotes, final.Options[0].Votes)
	}
}

// TestBroadcaster verifies basic subscribe,
// broadcast, and receive behavior.
func TestBroadcaster(t *testing.T) {
	broadcaster := NewBroadcaster()
	pollID := "test-poll"

	ch := broadcaster.Subscribe(pollID)

	go func() {
		broadcaster.Broadcast(pollID, "hello")
	}()

	select {
	case msg := <-ch:
		if msg != "hello" {
			t.Errorf("Expected 'hello', got '%s'", msg)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for message")
	}

	broadcaster.Unsubscribe(pollID, ch)
}

// TestBroadcasterMultipleSubscribers ensures
// all subscribers receive broadcast messages.
func TestBroadcasterMultipleSubscribers(t *testing.T) {
	broadcaster := NewBroadcaster()
	pollID := "test-poll"

	ch1 := broadcaster.Subscribe(pollID)
	ch2 := broadcaster.Subscribe(pollID)

	broadcaster.Broadcast(pollID, "test")

	for _, ch := range []chan string{ch1, ch2} {
		select {
		case msg := <-ch:
			if msg != "test" {
				t.Errorf("Expected 'test', got '%s'", msg)
			}
		case <-time.After(time.Second):
			t.Error("Timeout")
		}
	}

	broadcaster.Unsubscribe(pollID, ch1)
	broadcaster.Unsubscribe(pollID, ch2)
}

// TestCopyPoll verifies that copyPoll performs
// a deep copy and does not mutate the original poll.
func TestCopyPoll(t *testing.T) {
	original := &Poll{
		ID:       "123",
		Question: "Test?",
		Options:  []Option{{ID: "a", Text: "A", Votes: 5}},
	}

	copied := copyPoll(original)

	// Modify copy
	copied.Options[0].Votes = 100

	// Original should remain unchanged
	if original.Options[0].Votes != 5 {
		t.Error("Original was modified")
	}
}
