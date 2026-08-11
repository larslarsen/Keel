// SPDX-License-Identifier: Apache-2.0
package store

import "testing"

func TestQueueOrderRemoveReorder(t *testing.T) {
	st := openStore(t, "q.sqlite")
	for _, v := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"} {
		if err := st.AddToQueue(v, "yt", 1); err != nil {
			t.Fatal(err)
		}
	}
	ids := func() []string {
		items, err := st.ListQueue()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.VideoID
		}
		return out
	}
	if got := ids(); len(got) != 3 || got[0] != "aaaaaaaaaaa" || got[2] != "ccccccccccc" {
		t.Fatalf("queue order = %v, want insertion order", got)
	}

	// Adding twice is a user pressing a button twice, not a request to watch
	// something two times.
	if err := st.AddToQueue("aaaaaaaaaaa", "yt", 2); err != nil {
		t.Fatal(err)
	}
	if got := ids(); len(got) != 3 {
		t.Errorf("re-adding grew the queue to %v", got)
	}

	if err := st.ReorderQueue(2, 0); err != nil {
		t.Fatal(err)
	}
	if got := ids(); got[0] != "ccccccccccc" || got[1] != "aaaaaaaaaaa" {
		t.Errorf("after reorder queue = %v", got)
	}

	if err := st.RemoveFromQueue(1); err != nil {
		t.Fatal(err)
	}
	got := ids()
	if len(got) != 2 || got[0] != "ccccccccccc" || got[1] != "bbbbbbbbbbb" {
		t.Errorf("after removing index 1 queue = %v", got)
	}

	// Positions stay dense, so a later reorder cannot address a gap.
	items, _ := st.ListQueue()
	for i, it := range items {
		if it.Position != i {
			t.Errorf("position %d is %d — removal left a gap", i, it.Position)
		}
	}

	if err := st.RemoveFromQueue(9); err == nil {
		t.Error("removing a position that does not exist should fail")
	}
	if err := st.ReorderQueue(0, 9); err == nil {
		t.Error("reordering past the end should fail")
	}
}

func TestQueueSurvivesRestart(t *testing.T) {
	dir := t.TempDir() + "/q2.sqlite"
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddToQueue("dQw4w9WgXcQ", "yt", 7); err != nil {
		t.Fatal(err)
	}
	a.Close()

	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	items, err := b.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].VideoID != "dQw4w9WgXcQ" {
		t.Fatalf("queue did not survive a restart: %v", items)
	}
}

func TestAdvanceOnlyActsOnQueuedVideos(t *testing.T) {
	st := openStore(t, "adv.sqlite")
	for _, v := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"} {
		if err := st.AddToQueue(v, "yt", 1); err != nil {
			t.Fatal(err)
		}
	}

	// Finishing something that was never queued must not touch the queue. This
	// is what keeps autoplay from hijacking ordinary browsing.
	next, err := st.AdvanceQueue("zzzzzzzzzzz")
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Errorf("advanced on an unqueued video: %v", next)
	}
	if items, _ := st.ListQueue(); len(items) != 3 {
		t.Errorf("queue changed on an unqueued video: %v", items)
	}

	// Finishing a queued video consumes it and hands back the one after it.
	next, err = st.AdvanceQueue("aaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.VideoID != "bbbbbbbbbbb" {
		t.Fatalf("next = %v, want bbbbbbbbbbb", next)
	}
	items, _ := st.ListQueue()
	if len(items) != 2 || items[0].VideoID != "bbbbbbbbbbb" {
		t.Errorf("after advancing, queue = %v", items)
	}

	// Advancing from the middle plays what followed it, not the head.
	if next, _ = st.AdvanceQueue("bbbbbbbbbbb"); next == nil || next.VideoID != "ccccccccccc" {
		t.Fatalf("next = %v, want ccccccccccc", next)
	}

	// The last entry drains the queue and stops rather than looping.
	next, err = st.AdvanceQueue("ccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Errorf("the last queued video should stop, got %v", next)
	}
	if items, _ := st.ListQueue(); len(items) != 0 {
		t.Errorf("queue should be empty, got %v", items)
	}
}
