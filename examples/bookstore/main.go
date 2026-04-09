package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tomasweigenast/goserv"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := newStore()

	server := goserv.NewServer(
		goserv.WithPort(3000),
		goserv.WithFieldNamingConvention(goserv.SnakeCaseNaming),
		goserv.WithErrorHandler(func(err error, r *goserv.Context) goserv.Response {
			fmt.Printf("error %s %s: %v\n", r.Request().Method, r.Request().URL.Path, err)
			return goserv.DefaultErrorHandler(err, r)
		}),
	)

	server.RegisterRouteGroup("/authors", &AuthorRoutes{store: store})
	server.RegisterRouteGroup("/books", &BookRoutes{store: store})
	server.RegisterRouteGroup("/orders", &OrderRoutes{store: store})

	fmt.Println("")
	fmt.Println("── Bookstore API ────────────────────────────────────────────")
	fmt.Println("  Authors")
	fmt.Println("    GET    /authors              — list all authors (?page=&page_size=&search=)")
	fmt.Println("    GET    /authors/:id          — get author by ID")
	fmt.Println("    POST   /authors              — create author")
	fmt.Println("    DELETE /authors/:id          — delete author")
	fmt.Println("")
	fmt.Println("  Books")
	fmt.Println("    GET    /books                — list books (?author_id=&genre=&page=&page_size=)")
	fmt.Println("    GET    /books/:id            — get book by ID")
	fmt.Println("    POST   /books                — create book")
	fmt.Println("    PUT    /books/:id            — update book")
	fmt.Println("    DELETE /books/:id            — delete book")
	fmt.Println("")
	fmt.Println("  Orders")
	fmt.Println("    GET    /orders               — list orders (?status=)")
	fmt.Println("    GET    /orders/:id           — get order by ID")
	fmt.Println("    POST   /orders               — place order")
	fmt.Println("    POST   /orders/:id/cancel    — cancel order")
	fmt.Println("")

	if err := server.Listen(ctx); err != nil {
		log.Fatal(err)
	}
}

// ============================================================================
// Domain types
// ============================================================================

type Author struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Bio       string    `json:"bio,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Book struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	AuthorID    int64     `json:"author_id"`
	Genre       string    `json:"genre"`
	PublishedAt time.Time `json:"published_at"`
	Price       float64   `json:"price"`
	Stock       int       `json:"stock"`
}

type OrderItem struct {
	BookID   int64   `json:"book_id"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type Order struct {
	ID        int64       `json:"id"`
	Items     []OrderItem `json:"items"`
	Total     float64     `json:"total"`
	Status    string      `json:"status"` // "pending" | "confirmed" | "cancelled"
	CreatedAt time.Time   `json:"created_at"`
}

// ============================================================================
// In-memory store
// ============================================================================

type Store struct {
	mu      sync.RWMutex
	authors map[int64]*Author
	books   map[int64]*Book
	orders  map[int64]*Order
	nextID  int64
}

func newStore() *Store {
	s := &Store{
		authors: make(map[int64]*Author),
		books:   make(map[int64]*Book),
		orders:  make(map[int64]*Order),
		nextID:  1,
	}
	s.seed()
	return s
}

func (s *Store) nextSeq() int64 {
	id := s.nextID
	s.nextID++
	return id
}

func (s *Store) seed() {
	a1 := &Author{ID: s.nextSeq(), Name: "George Orwell", Bio: "English novelist and essayist.", CreatedAt: time.Now()}
	a2 := &Author{ID: s.nextSeq(), Name: "Frank Herbert", Bio: "American science fiction author.", CreatedAt: time.Now()}
	s.authors[a1.ID] = a1
	s.authors[a2.ID] = a2

	b1 := &Book{ID: s.nextSeq(), Title: "Nineteen Eighty-Four", AuthorID: a1.ID, Genre: "dystopia", PublishedAt: time.Date(1949, 6, 8, 0, 0, 0, 0, time.UTC), Price: 12.99, Stock: 50}
	b2 := &Book{ID: s.nextSeq(), Title: "Animal Farm", AuthorID: a1.ID, Genre: "allegory", PublishedAt: time.Date(1945, 8, 17, 0, 0, 0, 0, time.UTC), Price: 9.99, Stock: 30}
	b3 := &Book{ID: s.nextSeq(), Title: "Dune", AuthorID: a2.ID, Genre: "science fiction", PublishedAt: time.Date(1965, 8, 1, 0, 0, 0, 0, time.UTC), Price: 14.99, Stock: 100}
	s.books[b1.ID] = b1
	s.books[b2.ID] = b2
	s.books[b3.ID] = b3
}

// ============================================================================
// Author routes
// ============================================================================

type AuthorRoutes struct{ store *Store }

func (a *AuthorRoutes) Routes(g *goserv.RouteGroup) {
	g.Map("GET /", a.List)
	g.Map("GET /:id", a.Get)
	g.Map("POST /", a.Create)
	g.Map("DELETE /:id", a.Delete)
}

// PageQuery is a shared pagination struct that can be embedded in any
// request struct tagged goserv:"fromQuery". Its fields are flattened into
// the parent's query string: ?page=&page_size=
type PageQuery struct {
	Page     int // ?page=
	PageSize int // ?page_size=
}

// ListAuthorsRequest demonstrates nested fromQuery: Search is a scalar query
// param on the parent struct, and Paging is a nested struct whose fields are
// flattened into the same query string.
type ListAuthorsRequest struct {
	Search string    `goserv:"fromQuery"`          // ?search=
	Paging PageQuery `goserv:"fromQuery"`           // ?page=&page_size= (flattened)
}

// GET /authors?search=&page=&page_size=
func (a *AuthorRoutes) List(req ListAuthorsRequest) ([]*Author, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()

	page, pageSize := normalizePagination(req.Paging.Page, req.Paging.PageSize)
	search := strings.ToLower(req.Search)

	var results []*Author
	for _, author := range a.store.authors {
		if search != "" && !strings.Contains(strings.ToLower(author.Name), search) {
			continue
		}
		results = append(results, author)
	}

	return paginate(results, page, pageSize), nil
}

// GET /authors/:id
func (a *AuthorRoutes) Get(id int64) (*Author, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()

	author, ok := a.store.authors[id]
	if !ok {
		return nil, goserv.ErrNotFound("author not found")
	}
	return author, nil
}

type CreateAuthorRequest struct {
	Name string `json:"name"`
	Bio  string `json:"bio"`
}

// POST /authors
func (a *AuthorRoutes) Create(req CreateAuthorRequest) (goserv.Response, error) {
	if req.Name == "" {
		return nil, goserv.ErrUnprocessableEntity("validation failed").
			WithDetails(map[string]string{"name": "required"})
	}

	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	author := &Author{
		ID:        a.store.nextSeq(),
		Name:      req.Name,
		Bio:       req.Bio,
		CreatedAt: time.Now(),
	}
	a.store.authors[author.ID] = author

	return goserv.Created(author), nil
}

// DeleteAuthorRequest pulls the author ID from the path.
type DeleteAuthorRequest struct {
	ID int64 `goserv:"fromParam,id"`
}

// DELETE /authors/:id
func (a *AuthorRoutes) Delete(req DeleteAuthorRequest) (goserv.Response, error) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	if _, ok := a.store.authors[req.ID]; !ok {
		return nil, goserv.ErrNotFound("author not found")
	}

	// Prevent deletion when the author still has books.
	for _, b := range a.store.books {
		if b.AuthorID == req.ID {
			return nil, goserv.ErrConflict("author still has books; remove them first")
		}
	}

	delete(a.store.authors, req.ID)
	return goserv.NoContent(), nil
}

// ============================================================================
// Book routes
// ============================================================================

type BookRoutes struct{ store *Store }

func (b *BookRoutes) Routes(g *goserv.RouteGroup) {
	g.Map("GET /", b.List)
	g.Map("GET /:id", b.Get)
	g.Map("POST /", b.Create)
	g.Map("PUT /:id", b.Update)
	g.Map("DELETE /:id", b.Delete)
}

// ListBooksRequest combines filters and the shared PageQuery via nested fromQuery.
type ListBooksRequest struct {
	AuthorID int64     `goserv:"fromQuery"` // ?author_id=
	Genre    string    `goserv:"fromQuery"` // ?genre=
	Paging   PageQuery `goserv:"fromQuery"` // ?page=&page_size= (flattened)
}

// GET /books?author_id=&genre=&page=&page_size=
func (b *BookRoutes) List(req ListBooksRequest) ([]*Book, error) {
	b.store.mu.RLock()
	defer b.store.mu.RUnlock()

	page, pageSize := normalizePagination(req.Paging.Page, req.Paging.PageSize)
	genre := strings.ToLower(req.Genre)

	var results []*Book
	for _, book := range b.store.books {
		if req.AuthorID != 0 && book.AuthorID != req.AuthorID {
			continue
		}
		if genre != "" && !strings.EqualFold(book.Genre, genre) {
			continue
		}
		results = append(results, book)
	}

	return paginate(results, page, pageSize), nil
}

// GET /books/:id
func (b *BookRoutes) Get(id int64) (*Book, error) {
	b.store.mu.RLock()
	defer b.store.mu.RUnlock()

	book, ok := b.store.books[id]
	if !ok {
		return nil, goserv.ErrNotFound("book not found")
	}
	return book, nil
}

type CreateBookRequest struct {
	Title       string    `json:"title"`
	AuthorID    int64     `json:"author_id"`
	Genre       string    `json:"genre"`
	PublishedAt time.Time `json:"published_at"`
	Price       float64   `json:"price"`
	Stock       int       `json:"stock"`
}

// POST /books
func (b *BookRoutes) Create(req CreateBookRequest) (goserv.Response, error) {
	var errs = make(map[string]string)
	if req.Title == "" {
		errs["title"] = "required"
	}
	if req.AuthorID == 0 {
		errs["author_id"] = "required"
	}
	if req.Price < 0 {
		errs["price"] = "must be non-negative"
	}
	if len(errs) > 0 {
		return nil, goserv.ErrUnprocessableEntity("validation failed").WithDetails(errs)
	}

	b.store.mu.Lock()
	defer b.store.mu.Unlock()

	if _, ok := b.store.authors[req.AuthorID]; !ok {
		return nil, goserv.ErrBadRequest("author not found")
	}

	book := &Book{
		ID:          b.store.nextSeq(),
		Title:       req.Title,
		AuthorID:    req.AuthorID,
		Genre:       req.Genre,
		PublishedAt: req.PublishedAt,
		Price:       req.Price,
		Stock:       req.Stock,
	}
	b.store.books[book.ID] = book

	return goserv.Created(book), nil
}

// UpdateBookRequest combines path param and body.
type UpdateBookRequest struct {
	ID   int64           `goserv:"fromParam,id"`
	Body UpdateBookBody  `goserv:"fromBody"`
}

type UpdateBookBody struct {
	Title string  `json:"title"`
	Price float64 `json:"price"`
	Stock int     `json:"stock"`
	Genre string  `json:"genre"`
}

// PUT /books/:id
func (b *BookRoutes) Update(req UpdateBookRequest) (*Book, error) {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()

	book, ok := b.store.books[req.ID]
	if !ok {
		return nil, goserv.ErrNotFound("book not found")
	}

	if req.Body.Title != "" {
		book.Title = req.Body.Title
	}
	if req.Body.Price >= 0 {
		book.Price = req.Body.Price
	}
	if req.Body.Stock >= 0 {
		book.Stock = req.Body.Stock
	}
	if req.Body.Genre != "" {
		book.Genre = req.Body.Genre
	}

	return book, nil
}

// DELETE /books/:id
func (b *BookRoutes) Delete(id int64) (goserv.Response, error) {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()

	if _, ok := b.store.books[id]; !ok {
		return nil, goserv.ErrNotFound("book not found")
	}
	delete(b.store.books, id)
	return goserv.NoContent(), nil
}

// ============================================================================
// Order routes
// ============================================================================

type OrderRoutes struct{ store *Store }

func (o *OrderRoutes) Routes(g *goserv.RouteGroup) {
	g.Map("GET /", o.List)
	g.Map("GET /:id", o.Get)
	g.Map("POST /", o.Place)
	g.Map("POST /:id/cancel", o.Cancel)
}

// ListOrdersQuery filters orders by status.
type ListOrdersQuery struct {
	Status string // ?status=  (pending | confirmed | cancelled)
}

// GET /orders?status=
func (o *OrderRoutes) List(q goserv.Query[ListOrdersQuery]) ([]*Order, error) {
	o.store.mu.RLock()
	defer o.store.mu.RUnlock()

	var results []*Order
	for _, order := range o.store.orders {
		if q.Value.Status != "" && order.Status != q.Value.Status {
			continue
		}
		results = append(results, order)
	}
	return results, nil
}

// GET /orders/:id
func (o *OrderRoutes) Get(id int64) (*Order, error) {
	o.store.mu.RLock()
	defer o.store.mu.RUnlock()

	order, ok := o.store.orders[id]
	if !ok {
		return nil, goserv.ErrNotFound("order not found")
	}
	return order, nil
}

type PlaceOrderRequest struct {
	Items []PlaceOrderItem `json:"items"`
}

type PlaceOrderItem struct {
	BookID   int64 `json:"book_id"`
	Quantity int   `json:"quantity"`
}

// POST /orders
func (o *OrderRoutes) Place(req PlaceOrderRequest) (goserv.Response, error) {
	if len(req.Items) == 0 {
		return nil, goserv.ErrUnprocessableEntity("order must contain at least one item")
	}

	o.store.mu.Lock()
	defer o.store.mu.Unlock()

	var lineItems []OrderItem
	var total float64

	for _, item := range req.Items {
		if item.Quantity <= 0 {
			return nil, goserv.ErrBadRequest(fmt.Sprintf("quantity for book %d must be positive", item.BookID))
		}
		book, ok := o.store.books[item.BookID]
		if !ok {
			return nil, goserv.ErrBadRequest(fmt.Sprintf("book %d not found", item.BookID))
		}
		if book.Stock < item.Quantity {
			return nil, goserv.ErrConflict(fmt.Sprintf("insufficient stock for book %d", item.BookID))
		}
		book.Stock -= item.Quantity
		lineTotal := book.Price * float64(item.Quantity)
		total += lineTotal
		lineItems = append(lineItems, OrderItem{
			BookID:   book.ID,
			Quantity: item.Quantity,
			Price:    book.Price,
		})
	}

	order := &Order{
		ID:        o.store.nextSeq(),
		Items:     lineItems,
		Total:     total,
		Status:    "confirmed",
		CreatedAt: time.Now(),
	}
	o.store.orders[order.ID] = order

	return goserv.Created(order), nil
}

// CancelOrderRequest pulls the order ID from the path.
type CancelOrderRequest struct {
	ID int64 `goserv:"fromParam,id"`
}

// POST /orders/:id/cancel
func (o *OrderRoutes) Cancel(req CancelOrderRequest) (*Order, error) {
	o.store.mu.Lock()
	defer o.store.mu.Unlock()

	order, ok := o.store.orders[req.ID]
	if !ok {
		return nil, goserv.ErrNotFound("order not found")
	}
	if order.Status == "cancelled" {
		return nil, goserv.ErrConflict("order is already cancelled")
	}

	// Restock books.
	for _, item := range order.Items {
		if book, ok := o.store.books[item.BookID]; ok {
			book.Stock += item.Quantity
		}
	}

	order.Status = "cancelled"
	return order, nil
}

// ============================================================================
// Pagination helpers
// ============================================================================

func normalizePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize
}

// paginate returns a slice of T for the requested page. T is constrained to
// any pointer type so this works with both *Author, *Book, and *Order.
func paginate[T any](items []T, page, pageSize int) []T {
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []T{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}
