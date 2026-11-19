package manager

import (
	"errors"
	"io"
	"log"
	"slices"
	"testing"
	"time"

	"github.com/knadh/listmonk/models"
)

type dailyLimitTestStore struct {
	Store
	attempts int
	claimed  [9]bool
	counts   chan int
}

func (s *dailyLimitTestStore) NextSubscribers(_, afterID, _ int) ([]models.Subscriber, error) {
	if afterID > 0 {
		return nil, nil
	}
	subs := make([]models.Subscriber, 8)
	for i := range subs {
		subs[i].ID = i + 1
	}
	return subs, nil
}

func (s *dailyLimitTestStore) ClaimCampaignRecipient(_, id int) (bool, error) {
	if s.claimed[id] {
		return false, nil
	}
	if s.attempts == 7 {
		return false, ErrDailyLimitReached
	}
	s.claimed[id] = true
	s.attempts++
	return true, nil
}

func (s *dailyLimitTestStore) UpdateCampaignCounts(_, _, sent int) error {
	s.counts <- sent
	return nil
}

type blockedMessenger struct {
	Messenger
	started chan struct{}
	release chan struct{}
	ids     []int
}

func (m *blockedMessenger) Name() string { return "test" }
func (m *blockedMessenger) Push(msg models.Message) error {
	if len(m.ids) == 0 {
		close(m.started)
	}
	<-m.release
	m.ids = append(m.ids, msg.Subscriber.ID)
	return nil
}

func TestDailyLimitDrainsQueuedMessages(t *testing.T) {
	store := &dailyLimitTestStore{counts: make(chan int, 1)}
	m := New(Config{Concurrency: 1, MessageRate: 10}, store, nil, log.New(io.Discard, "", 0))
	sender := &blockedMessenger{started: make(chan struct{}), release: make(chan struct{})}
	if err := m.AddMessenger(sender); err != nil {
		t.Fatal(err)
	}
	p, err := m.newPipe(&models.Campaign{Messenger: sender.Name(), Body: "test", DailyLimit: 7})
	if err != nil {
		t.Fatal(err)
	}
	go m.Run()
	defer m.Close()
	m.nextPipes <- p
	select {
	case <-sender.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first worker did not start")
	}
	close(sender.release)
	select {
	case counts := <-store.counts:
		if counts != 7 {
			t.Fatalf("sent: %v, want 7", counts)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("campaign did not drain")
	}
	if len(sender.ids) != 7 {
		t.Fatalf("delivered %v, want all seven recipients", sender.ids)
	}
	for i, id := range sender.ids {
		if id != i+1 {
			t.Fatalf("unexpected delivery order or duplicate: %v", sender.ids)
		}
	}
}

// A rejected attempt uses one slot. All unattempted recipients survive a paused
// pipe and can use the remaining six slots when a new pipe resumes it.
func TestSMTPErrorPauseThenResume(t *testing.T) {
	store := &dailyLimitTestStore{counts: make(chan int, 1)}
	sender := &rejectFirstMessenger{}
	for window, wantSent := range []int{0, 6, 1} {
		if window == 2 {
			store.attempts = 0
		}
		m := New(Config{Concurrency: 1, MessageRate: 20, MaxSendErrors: 1}, store, nil, log.New(io.Discard, "", 0))
		m.fnNotify = func(string, any) error { return nil }
		if err := m.AddMessenger(sender); err != nil {
			t.Fatal(err)
		}
		p, err := m.newPipe(&models.Campaign{Messenger: sender.Name(), Body: "test", DailyLimit: 7})
		if err != nil {
			t.Fatal(err)
		}
		go m.Run()
		m.nextPipes <- p
		deadline := time.Now().Add(3 * time.Second)
		for m.HasRunningCampaigns() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if m.HasRunningCampaigns() {
			t.Fatal("campaign did not drain")
		}
		m.Close()
		if got := <-store.counts; got != wantSent {
			t.Fatalf("run %d: sent %d, want %d", window, got, wantSent)
		}
		if window == 0 && !p.withErrors.Load() {
			t.Fatal("expected automatic pause")
		}
	}
	if !slices.Equal(sender.ids, []int{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("attempted IDs: %v", sender.ids)
	}
}

func (s *dailyLimitTestStore) GetCampaign(int) (*models.Campaign, error) {
	return &models.Campaign{Status: models.CampaignStatusRunning}, nil
}
func (s *dailyLimitTestStore) UpdateCampaignStatus(int, string) error { return nil }

type rejectFirstMessenger struct {
	Messenger
	ids []int
}

func (*rejectFirstMessenger) Name() string { return "smtp-test" }
func (m *rejectFirstMessenger) Push(msg models.Message) error {
	m.ids = append(m.ids, msg.Subscriber.ID)
	if len(m.ids) == 1 {
		return errors.New("SMTP rejected recipient")
	}
	return nil
}
