package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/ericlys/goReact/internal/store/pgstore"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

type apiHandler struct {
	q           *pgstore.Queries //todo: change to interface
	r           *chi.Mux
	upgrader    websocket.Upgrader
	subscribers map[string]map[*websocket.Conn]context.CancelFunc
	// block access to subscribers
	mu *sync.Mutex
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.r.ServeHTTP(w, r)
}

func NewHandler(q *pgstore.Queries) http.Handler {
	a := apiHandler{
		q: q,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true //cors
			},
		},
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc), // initialize subscribers with empty map
		mu:          &sync.Mutex{},                                           // initialize mutex with new mutex
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, middleware.Logger)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/subscribe/{room_id}", a.handleSubscribe) //fora da api pq é um websocket

	r.Route("/api", func(r chi.Router) {
		r.Route("/rooms", func(r chi.Router) {
			r.Post("/", a.handleCreateRoom)
			r.Get("/", a.handleGetRooms)

			r.Route("/{room_id}/messages", func(r chi.Router) {
				r.Post("/", a.handleCreateRoomMessages)
				r.Get("/", a.handleGetRoomMessages)

				r.Route("/{message_id}", func(r chi.Router) {
					r.Get("/", a.handleGetRoomMessage)
					r.Patch("/react", a.handleReactToMessage)
					r.Delete("/react", a.handleRemoveReactFromMessage)
					r.Patch("/answer", a.handleMarkMessageAsAnswered)
				})
			})

		})
	})

	a.r = r
	return a
}

const (
	MessageKindMessageCreated = "message_created"
)

type MessageMessageCreaged struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type Message struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

func (h apiHandler) notifyClient(msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subscribers, ok := h.subscribers[msg.RoomID]

	if !ok || len(subscribers) == 0 {
		return
	}

	for conn, cancel := range subscribers {
		if err := conn.WriteJSON(msg); err != nil {
			slog.Warn("failed to send message to client", "error", err)
			cancel()
		}
	}

}

func (h apiHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	rawRoomId := chi.URLParam(r, "room_id")
	roomId, err := uuid.Parse(rawRoomId)

	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	// check if room exists
	_, err = h.q.GetRoom(r.Context(), roomId)

	// if not, return not found
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "room not found", http.StatusBadRequest)
			return
		}
		// else, something went wrong
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	// upgrade connection to websocket
	c, err := h.upgrader.Upgrade(w, r, nil)

	// if upgrade fails, return error
	if err != nil {
		// log error to server
		slog.Warn("failed to upgrade connection", slog.String("error", err.Error()))
		// return error to client
		http.Error(w, "failed to upgrade connection", http.StatusInternalServerError)
	}

	// close connection when done with client
	defer c.Close()

	ctx, cancel := context.WithCancel(r.Context())

	h.mu.Lock()
	if _, ok := h.subscribers[rawRoomId]; !ok {
		// initialize a new connection in room
		h.subscribers[rawRoomId] = make(map[*websocket.Conn]context.CancelFunc)
	}

	slog.Info("new client connected", "room_id", rawRoomId, "client_ip", r.RemoteAddr)

	// declare a new connection in room with an cancel function
	h.subscribers[rawRoomId][c] = cancel
	h.mu.Unlock()

	// wait for context signal (client or server) to close connection
	<-ctx.Done()

	//if code gets here, context is done
	// if context is done, remove connection from room
	h.mu.Lock()
	delete(h.subscribers[rawRoomId], c)
	h.mu.Unlock()
}

func (h apiHandler) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	type _body struct {
		Theme string `json:"theme"`
	}

	var body _body
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	roomId, err := h.q.InsertRoom(r.Context(), body.Theme)

	if err != nil {
		slog.Error("failed to create room", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, _ := json.Marshal(response{ID: roomId.String()})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h apiHandler) handleGetRooms(w http.ResponseWriter, r *http.Request) {

}
func (h apiHandler) handleCreateRoomMessages(w http.ResponseWriter, r *http.Request) {
	rawRoomId := chi.URLParam(r, "room_id")
	roomId, err := uuid.Parse(rawRoomId)

	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	// check if room exists
	_, err = h.q.GetRoom(r.Context(), roomId)

	// if not, return not found
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "room not found", http.StatusBadRequest)
			return
		}
		// else, something went wrong
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type _body struct {
		Message string `json:"message"`
	}

	var body _body
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	messageId, err := h.q.InsertMessage(r.Context(), pgstore.InsertMessageParams{RoomID: roomId, Message: body.Message})

	if err != nil {
		slog.Error("failed to insert message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, _ := json.Marshal(response{ID: messageId.String()})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)

	go h.notifyClient(Message{
		Kind:   MessageKindMessageCreated,
		RoomID: rawRoomId,
		Value:  MessageMessageCreaged{ID: messageId.String(), Message: body.Message},
	})
}

func (h apiHandler) handleGetRoomMessages(w http.ResponseWriter, r *http.Request) {

}

func (h apiHandler) handleGetRoomMessage(w http.ResponseWriter, r *http.Request) {

}

func (h apiHandler) handleReactToMessage(w http.ResponseWriter, r *http.Request) {

}

func (h apiHandler) handleRemoveReactFromMessage(w http.ResponseWriter, r *http.Request) {

}

func (h apiHandler) handleMarkMessageAsAnswered(w http.ResponseWriter, r *http.Request) {

}
