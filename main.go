// main.go - Real-time Polling Application
// A simple yet practical polling app with live results using SSE
// Run: go run main.go
// Test: go test -v

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// MODELS
// ============================================================================

// Poll represents a voting poll with multiple options
type Poll struct {
	ID        string    `json:"id"`
	Question  string    `json:"question"`
	Options   []Option  `json:"options"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Option represents a single voting option
type Option struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Votes int    `json:"votes"`
}

// TotalVotes returns the total number of votes in a poll
func (p *Poll) TotalVotes() int {
	total := 0
	for _, opt := range p.Options {
		total += opt.Votes
	}
	return total
}

// VotePercentage calculates the percentage for an option
func (p *Poll) VotePercentage(optionID string) float64 {
	total := p.TotalVotes()
	if total == 0 {
		return 0
	}
	for _, opt := range p.Options {
		if opt.ID == optionID {
			return float64(opt.Votes) / float64(total) * 100
		}
	}
	return 0
}

// IsExpired checks if the poll has expired
func (p *Poll) IsExpired() bool {
	if p.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(p.ExpiresAt)
}

// ============================================================================
// STORAGE (In-Memory with Thread Safety)
// ============================================================================

// Store handles thread-safe poll storage
type Store struct {
	polls map[string]*Poll
	mu    sync.RWMutex
}

// NewStore creates a new poll store
func NewStore() *Store {
	return &Store{
		polls: make(map[string]*Poll),
	}
}

// Create adds a new poll to the store
func (s *Store) Create(poll *Poll) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poll.ID == "" {
		poll.ID = generateID()
	}
	poll.CreatedAt = time.Now()
	s.polls[poll.ID] = poll
	return nil
}

// Get retrieves a poll by ID
func (s *Store) Get(id string) (*Poll, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	poll, exists := s.polls[id]
	if !exists {
		return nil, false
	}
	return copyPoll(poll), true
}

// Vote adds a vote to an option
func (s *Store) Vote(pollID, optionID string) (*Poll, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	poll, exists := s.polls[pollID]
	if !exists {
		return nil, fmt.Errorf("poll not found")
	}

	if poll.IsExpired() {
		return nil, fmt.Errorf("poll has expired")
	}

	for i := range poll.Options {
		if poll.Options[i].ID == optionID {
			poll.Options[i].Votes++
			return copyPoll(poll), nil
		}
	}
	return nil, fmt.Errorf("option not found")
}

// List returns all polls sorted by creation date (newest first)
func (s *Store) List() []*Poll {
	s.mu.RLock()
	defer s.mu.RUnlock()

	polls := make([]*Poll, 0, len(s.polls))
	for _, p := range s.polls {
		polls = append(polls, copyPoll(p))
	}

	sort.Slice(polls, func(i, j int) bool {
		return polls[i].CreatedAt.After(polls[j].CreatedAt)
	})

	return polls
}

// Delete removes a poll from the store
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.polls[id]; exists {
		delete(s.polls, id)
		return true
	}
	return false
}

// copyPoll creates a deep copy of a poll
func copyPoll(p *Poll) *Poll {
	options := make([]Option, len(p.Options))
	copy(options, p.Options)
	return &Poll{
		ID:        p.ID,
		Question:  p.Question,
		Options:   options,
		CreatedAt: p.CreatedAt,
		ExpiresAt: p.ExpiresAt,
	}
}

// ============================================================================
// SSE (Server-Sent Events) BROADCASTER
// ============================================================================

// Broadcaster manages SSE connections for real-time updates
type Broadcaster struct {
	subscribers map[string]map[chan string]bool
	mu          sync.RWMutex
}

// NewBroadcaster creates a new SSE broadcaster
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subscribers: make(map[string]map[chan string]bool),
	}
}

// Subscribe creates a new subscription for a poll
func (b *Broadcaster) Subscribe(pollID string) chan string {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan string, 10)
	if b.subscribers[pollID] == nil {
		b.subscribers[pollID] = make(map[chan string]bool)
	}
	b.subscribers[pollID][ch] = true

	log.Printf("New subscriber for poll %s (total: %d)", pollID, len(b.subscribers[pollID]))
	return ch
}

// Unsubscribe removes a subscription
func (b *Broadcaster) Unsubscribe(pollID string, ch chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if subs, exists := b.subscribers[pollID]; exists {
		delete(subs, ch)
		close(ch)
		log.Printf("Unsubscribed from poll %s (remaining: %d)", pollID, len(subs))
	}
}

// Broadcast sends data to all subscribers of a poll
func (b *Broadcaster) Broadcast(pollID string, data string) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if subs, exists := b.subscribers[pollID]; exists {
		for ch := range subs {
			select {
			case ch <- data:
			default:
				// Channel is full, skip
			}
		}
	}
}

// ============================================================================
// HTTP HANDLERS
// ============================================================================

// App holds application dependencies
type App struct {
	store       *Store
	broadcaster *Broadcaster
}

// NewApp creates a new application instance
func NewApp() *App {
	return &App{
		store:       NewStore(),
		broadcaster: NewBroadcaster(),
	}
}

// IndexHandler displays the home page with all polls
func (app *App) IndexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	polls := app.store.List()
	tmpl := template.Must(template.New("index").Parse(indexTemplate))
	tmpl.Execute(w, map[string]interface{}{
		"Polls": polls,
	})
}

// CreateHandler handles poll creation
func (app *App) CreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		tmpl := template.Must(template.New("create").Parse(createTemplate))
		tmpl.Execute(w, nil)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	question := strings.TrimSpace(r.FormValue("question"))
	optionsRaw := r.FormValue("options")

	if question == "" || optionsRaw == "" {
		http.Error(w, "Question and options are required", http.StatusBadRequest)
		return
	}

	optionTexts := strings.Split(optionsRaw, "\n")
	var options []Option
	for _, text := range optionTexts {
		text = strings.TrimSpace(text)
		if text != "" {
			options = append(options, Option{
				ID:   generateID(),
				Text: text,
			})
		}
	}

	if len(options) < 2 {
		http.Error(w, "At least 2 options are required", http.StatusBadRequest)
		return
	}

	poll := &Poll{
		Question: question,
		Options:  options,
	}

	if expiry := r.FormValue("expiry"); expiry != "" {
		if hours, err := time.ParseDuration(expiry + "h"); err == nil {
			poll.ExpiresAt = time.Now().Add(hours)
		}
	}

	app.store.Create(poll)
	log.Printf("Created poll: %s - %s", poll.ID, poll.Question)

	http.Redirect(w, r, "/poll/"+poll.ID, http.StatusSeeOther)
}

// PollHandler displays a single poll
func (app *App) PollHandler(w http.ResponseWriter, r *http.Request) {
	pollID := strings.TrimPrefix(r.URL.Path, "/poll/")
	poll, exists := app.store.Get(pollID)
	if !exists {
		http.Error(w, "Poll not found", http.StatusNotFound)
		return
	}

	funcMap := template.FuncMap{
		"percentage": func(optID string) float64 {
			return poll.VotePercentage(optID)
		},
	}

	tmpl := template.Must(template.New("poll").Funcs(funcMap).Parse(pollTemplate))
	tmpl.Execute(w, poll)
}

// VoteHandler handles voting
func (app *App) VoteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pollID := strings.TrimPrefix(r.URL.Path, "/vote/")
	optionID := r.FormValue("option")

	if optionID == "" {
		http.Error(w, "Option is required", http.StatusBadRequest)
		return
	}

	poll, err := app.store.Vote(pollID, optionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Vote recorded for poll %s, option %s", pollID, optionID)

	data, _ := json.Marshal(poll)
	app.broadcaster.Broadcast(pollID, string(data))

	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(poll)
		return
	}

	http.Redirect(w, r, "/poll/"+pollID, http.StatusSeeOther)
}

// EventsHandler handles SSE connections for real-time updates
func (app *App) EventsHandler(w http.ResponseWriter, r *http.Request) {
	pollID := strings.TrimPrefix(r.URL.Path, "/events/")

	if _, exists := app.store.Get(pollID); !exists {
		http.Error(w, "Poll not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := app.broadcaster.Subscribe(pollID)
	defer app.broadcaster.Unsubscribe(pollID, ch)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	poll, _ := app.store.Get(pollID)
	data, _ := json.Marshal(poll)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// APIListHandler returns all polls as JSON
func (app *App) APIListHandler(w http.ResponseWriter, r *http.Request) {
	polls := app.store.List()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(polls)
}

// ============================================================================
// UTILITIES
// ============================================================================

func generateID() string {
	bytes := make([]byte, 4)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// ============================================================================
// HTML TEMPLATES (with Tailwind CSS)
// ============================================================================

const baseStyle = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>QuickPoll</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <style>
        .vote-bar { transition: width 0.5s ease-in-out; }
    </style>
</head>
<body class="bg-gradient-to-br from-indigo-100 via-purple-50 to-pink-100 min-h-screen">`

const baseEnd = `</body></html>`

const indexTemplate = baseStyle + `
    <div class="container mx-auto px-4 py-8 max-w-4xl">
        <header class="text-center mb-12">
            <h1 class="text-5xl font-bold bg-gradient-to-r from-indigo-600 to-purple-600 bg-clip-text text-transparent mb-4">
                🗳️ QuickPoll
            </h1>
            <p class="text-gray-600 text-lg">Create instant polls with real-time results</p>
        </header>

        <div class="text-center mb-8">
            <a href="/create" class="inline-flex items-center px-6 py-3 bg-gradient-to-r from-indigo-600 to-purple-600 text-white font-semibold rounded-xl shadow-lg hover:shadow-xl transform hover:-translate-y-1 transition-all duration-200">
                <svg class="w-5 h-5 mr-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/>
                </svg>
                Create New Poll
            </a>
        </div>

        <div class="space-y-4">
            {{if .Polls}}
                <h2 class="text-2xl font-semibold text-gray-800 mb-4">Recent Polls</h2>
                {{range .Polls}}
                <a href="/poll/{{.ID}}" class="block bg-white rounded-xl shadow-md hover:shadow-lg transition-shadow duration-200 p-6 border border-gray-100">
                    <div class="flex justify-between items-start">
                        <div class="flex-1">
                            <h3 class="text-xl font-semibold text-gray-800 mb-2">{{.Question}}</h3>
                            <div class="flex items-center text-sm text-gray-500 space-x-4">
                                <span>{{.TotalVotes}} votes</span>
                                <span>{{len .Options}} options</span>
                            </div>
                        </div>
                        <span class="inline-flex items-center px-3 py-1 rounded-full text-xs font-medium {{if .IsExpired}}bg-red-100 text-red-800{{else}}bg-green-100 text-green-800{{end}}">
                            {{if .IsExpired}}Expired{{else}}Active{{end}}
                        </span>
                    </div>
                </a>
                {{end}}
            {{else}}
                <div class="text-center py-12 bg-white rounded-xl shadow-md">
                    <h3 class="text-lg font-medium text-gray-900 mb-2">No polls yet</h3>
                    <p class="text-gray-500">Create your first poll to get started!</p>
                </div>
            {{end}}
        </div>
    </div>
` + baseEnd

const createTemplate = baseStyle + `
    <div class="container mx-auto px-4 py-8 max-w-2xl">
        <a href="/" class="inline-flex items-center text-indigo-600 hover:text-indigo-800 mb-6">
            ← Back to polls
        </a>

        <div class="bg-white rounded-2xl shadow-xl p-8">
            <h1 class="text-3xl font-bold text-gray-800 mb-6">Create a New Poll</h1>
            
            <form method="POST" action="/create" class="space-y-6">
                <div>
                    <label for="question" class="block text-sm font-medium text-gray-700 mb-2">
                        Your Question
                    </label>
                    <input type="text" id="question" name="question" required
                        class="w-full px-4 py-3 border border-gray-300 rounded-xl focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500"
                        placeholder="What would you like to ask?">
                </div>

                <div>
                    <label for="options" class="block text-sm font-medium text-gray-700 mb-2">
                        Options (one per line, minimum 2)
                    </label>
                    <textarea id="options" name="options" rows="5" required
                        class="w-full px-4 py-3 border border-gray-300 rounded-xl focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500"
                        placeholder="Option 1&#10;Option 2&#10;Option 3"></textarea>
                </div>

                <div>
                    <label for="expiry" class="block text-sm font-medium text-gray-700 mb-2">
                        Expires in (optional)
                    </label>
                    <select id="expiry" name="expiry"
                        class="w-full px-4 py-3 border border-gray-300 rounded-xl focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500">
                        <option value="">Never</option>
                        <option value="1">1 hour</option>
                        <option value="24">24 hours</option>
                        <option value="168">1 week</option>
                    </select>
                </div>

                <button type="submit"
                    class="w-full py-3 px-6 bg-gradient-to-r from-indigo-600 to-purple-600 text-white font-semibold rounded-xl shadow-lg hover:shadow-xl transform hover:-translate-y-0.5 transition-all duration-200">
                    Create Poll
                </button>
            </form>
        </div>
    </div>
` + baseEnd

const pollTemplate = baseStyle + `
    <div class="container mx-auto px-4 py-8 max-w-2xl">
        <a href="/" class="inline-flex items-center text-indigo-600 hover:text-indigo-800 mb-6">
            ← Back to polls
        </a>

        <div class="bg-white rounded-2xl shadow-xl p-8">
            <div class="flex items-start justify-between mb-6">
                <h1 class="text-2xl font-bold text-gray-800">{{.Question}}</h1>
                <span id="live-indicator" class="inline-flex items-center px-3 py-1 rounded-full text-xs font-medium bg-green-100 text-green-800">
                    <span class="w-2 h-2 bg-green-500 rounded-full mr-2 animate-pulse"></span>
                    Live
                </span>
            </div>

            <p class="text-gray-500 mb-6" id="total-votes">Total votes: {{.TotalVotes}}</p>

            <form id="vote-form" method="POST" action="/vote/{{.ID}}" class="space-y-4">
                <div id="options-container" class="space-y-3">
                    {{range .Options}}
                    <div class="option-item relative" data-option-id="{{.ID}}">
                        <input type="radio" id="opt-{{.ID}}" name="option" value="{{.ID}}" 
                            class="sr-only peer" {{if $.IsExpired}}disabled{{end}}>
                        <label for="opt-{{.ID}}" 
                            class="block p-4 border-2 border-gray-200 rounded-xl cursor-pointer 
                                   peer-checked:border-indigo-500 peer-checked:bg-indigo-50 
                                   hover:border-gray-300 transition-colors {{if $.IsExpired}}opacity-50 cursor-not-allowed{{end}}">
                            <div class="flex justify-between items-center mb-2">
                                <span class="font-medium text-gray-800">{{.Text}}</span>
                                <span class="vote-count text-sm text-gray-500">{{.Votes}} votes</span>
                            </div>
                            <div class="h-2 bg-gray-200 rounded-full overflow-hidden">
                                <div class="vote-bar h-full bg-gradient-to-r from-indigo-500 to-purple-500 rounded-full" 
                                     style="width: {{printf "%.1f" (percentage .ID)}}%"></div>
                            </div>
                            <div class="text-right mt-1">
                                <span class="vote-percentage text-xs text-gray-500">{{printf "%.1f" (percentage .ID)}}%</span>
                            </div>
                        </label>
                    </div>
                    {{end}}
                </div>

                {{if not .IsExpired}}
                <button type="submit" id="vote-btn"
                    class="w-full py-3 px-6 bg-gradient-to-r from-indigo-600 to-purple-600 text-white font-semibold rounded-xl shadow-lg hover:shadow-xl transform hover:-translate-y-0.5 transition-all duration-200">
                    Vote
                </button>
                {{else}}
                <div class="text-center py-4 bg-red-50 rounded-xl">
                    <span class="text-red-600 font-medium">This poll has expired</span>
                </div>
                {{end}}
            </form>

            <div class="mt-8 pt-6 border-t border-gray-200">
                <p class="text-sm text-gray-500 mb-2">Share this poll:</p>
                <div class="flex items-center space-x-2">
                    <input type="text" id="share-url" readonly
                        class="flex-1 px-4 py-2 bg-gray-50 border border-gray-300 rounded-lg text-sm text-gray-600">
                    <button onclick="copyShareUrl()" 
                        class="px-4 py-2 bg-gray-100 hover:bg-gray-200 text-gray-700 rounded-lg transition-colors">
                        Copy
                    </button>
                </div>
            </div>
        </div>
    </div>

    <script>
        document.getElementById('share-url').value = window.location.href;

        function copyShareUrl() {
            var input = document.getElementById('share-url');
            input.select();
            document.execCommand('copy');
            alert('Link copied!');
        }

        var pollId = '{{.ID}}';
        var evtSource = new EventSource('/events/' + pollId);

        evtSource.onmessage = function(event) {
            var poll = JSON.parse(event.data);
            updatePollUI(poll);
        };

        evtSource.onerror = function(err) {
            console.error('SSE error:', err);
        };

        function updatePollUI(poll) {
            var total = poll.options.reduce(function(sum, opt) { return sum + opt.votes; }, 0);
            document.getElementById('total-votes').textContent = 'Total votes: ' + total;

            poll.options.forEach(function(opt) {
                var container = document.querySelector('[data-option-id="' + opt.id + '"]');
                if (container) {
                    var percentage = total > 0 ? (opt.votes / total * 100) : 0;
                    container.querySelector('.vote-count').textContent = opt.votes + ' votes';
                    container.querySelector('.vote-bar').style.width = percentage + '%';
                    container.querySelector('.vote-percentage').textContent = percentage.toFixed(1) + '%';
                }
            });
        }

        document.getElementById('vote-form').addEventListener('submit', function(e) {
            e.preventDefault();
            
            var formData = new FormData(this);
            var selectedOption = formData.get('option');
            
            if (!selectedOption) {
                alert('Please select an option');
                return;
            }

            var btn = document.getElementById('vote-btn');
            btn.disabled = true;
            btn.textContent = 'Voting...';

            fetch('/vote/' + pollId, {
                method: 'POST',
                headers: { 'Accept': 'application/json' },
                body: formData
            })
            .then(function(response) {
                if (response.ok) return response.json();
                throw new Error('Vote failed');
            })
            .then(function(poll) {
                updatePollUI(poll);
                btn.textContent = 'Voted! ✓';
                btn.className = btn.className.replace('from-indigo-600', 'from-green-500').replace('to-purple-600', 'to-green-600');
            })
            .catch(function(err) {
                console.error('Error:', err);
                btn.disabled = false;
                btn.textContent = 'Vote';
                alert('Failed to submit vote');
            });
        });

        window.addEventListener('beforeunload', function() {
            evtSource.close();
        });
    </script>
` + baseEnd

// ============================================================================
// MAIN ENTRY POINT
// ============================================================================

func main() {
	app := NewApp()

	// Add sample polls
	app.store.Create(&Poll{
		Question: "What's your favorite programming language?",
		Options: []Option{
			{ID: generateID(), Text: "Go", Votes: 42},
			{ID: generateID(), Text: "Python", Votes: 38},
			{ID: generateID(), Text: "Rust", Votes: 25},
			{ID: generateID(), Text: "TypeScript", Votes: 31},
		},
	})

	app.store.Create(&Poll{
		Question: "Best time for team meetings?",
		Options: []Option{
			{ID: generateID(), Text: "Morning (9-11 AM)", Votes: 15},
			{ID: generateID(), Text: "Afternoon (2-4 PM)", Votes: 22},
			{ID: generateID(), Text: "Late afternoon (4-6 PM)", Votes: 8},
		},
	})

	// Setup routes
	http.HandleFunc("/", app.IndexHandler)
	http.HandleFunc("/create", app.CreateHandler)
	http.HandleFunc("/poll/", app.PollHandler)
	http.HandleFunc("/vote/", app.VoteHandler)
	http.HandleFunc("/events/", app.EventsHandler)
	http.HandleFunc("/api/polls", app.APIListHandler)

	addr := ":8080"
	log.Printf("🚀 QuickPoll server starting on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
