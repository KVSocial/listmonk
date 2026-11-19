package manager

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/knadh/listmonk/models"
)

type dailyLimitTestStore struct {
	Store
	fetched bool
	counts  chan [2]int
}

func (s *dailyLimitTestStore) NextSubscribers(int, int) ([]models.Subscriber, error) {
	if s.fetched {
		return nil, ErrDailyLimitReached
	}
	s.fetched = true
	subs := make([]models.Subscriber, 7)
	for i := range subs {
		subs[i].ID = i + 1
	}
	return subs, nil
}

func (s *dailyLimitTestStore) UpdateCampaignCounts(_, _, sent, lastID int) error {
	s.counts <- [2]int{sent, lastID}
	return nil
}

type blockedMessenger struct {
	Messenger
	release chan struct{}
	ids     []int
}

func (m *blockedMessenger) Name() string { return "test" }
func (m *blockedMessenger) Push(msg models.Message) error {
	<-m.release
	m.ids = append(m.ids, msg.Subscriber.ID)
	return nil
}

func TestDailyLimitDrainsQueuedMessages(t *testing.T) {
	store := &dailyLimitTestStore{counts: make(chan [2]int, 1)}
	m := New(Config{Concurrency: 1, MessageRate: 10}, store, nil, log.New(io.Discard, "", 0))
	sender := &blockedMessenger{release: make(chan struct{})}
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
	deadline := time.Now().Add(3 * time.Second)
	for !p.waitingForWindow.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(sender.release)
	select {
	case counts := <-store.counts:
		if counts != [2]int{7, 0} {
			t.Fatalf("sent and checkpoint override: %v, want [7 0]", counts)
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
