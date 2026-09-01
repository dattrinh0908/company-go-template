package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"order-service/internal/domain"
)

// OrderHandler serves the /orders resource.
type OrderHandler struct {
	orders *domain.OrderService
}

// NewOrderHandler wires a handler to the order use cases.
func NewOrderHandler(orders *domain.OrderService) *OrderHandler {
	return &OrderHandler{orders: orders}
}

// Register mounts the resource on a router group.
func (h *OrderHandler) Register(r gin.IRouter) {
	group := r.Group("/orders")
	group.POST("", h.create)
	group.GET("", h.list)
	group.GET("/:id", h.get)
	group.PATCH("/:id/status", h.updateStatus)
	group.DELETE("/:id", h.delete)
}

// createItemRequest is one requested line. The binding tags catch shape errors
// early; domain.Order.Validate is what enforces the business invariants, and it
// stays authoritative even if this transport is replaced.
type createItemRequest struct {
	SKU       string `json:"sku" binding:"required"`
	Name      string `json:"name" binding:"required"`
	Quantity  int    `json:"quantity" binding:"required,gt=0"`
	UnitPrice int64  `json:"unit_price" binding:"gte=0"`
}

type createOrderRequest struct {
	CustomerID string              `json:"customer_id" binding:"required,uuid"`
	Currency   string              `json:"currency" binding:"required,len=3"`
	Items      []createItemRequest `json:"items" binding:"required,min=1,dive"`
}

func (h *OrderHandler) create(c *gin.Context) {
	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, bindingError(err))
		return
	}

	customerID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		v := &domain.ValidationError{}
		v.Add("customer_id", "must be a valid UUID")
		respondError(c, v)
		return
	}

	items := make([]domain.NewItemInput, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, domain.NewItemInput{
			SKU:       item.SKU,
			Name:      item.Name,
			Quantity:  item.Quantity,
			UnitPrice: item.UnitPrice,
		})
	}

	order, err := h.orders.CreateOrder(c.Request.Context(), domain.CreateOrderInput{
		CustomerID: customerID,
		Currency:   req.Currency,
		Items:      items,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	c.Header("Location", "/api/v1/orders/"+order.ID.String())
	respond(c, http.StatusCreated, order)
}

func (h *OrderHandler) get(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	order, err := h.orders.GetOrder(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, order)
}

func (h *OrderHandler) list(c *gin.Context) {
	filter := domain.ListFilter{
		Limit:  queryInt(c, "limit", domain.DefaultPageLimit),
		Offset: queryInt(c, "offset", 0),
	}

	if raw := c.Query("customer_id"); raw != "" {
		customerID, err := uuid.Parse(raw)
		if err != nil {
			v := &domain.ValidationError{}
			v.Add("customer_id", "must be a valid UUID")
			respondError(c, v)
			return
		}
		filter.CustomerID = &customerID
	}
	if raw := c.Query("status"); raw != "" {
		status := domain.Status(raw)
		filter.Status = &status
	}

	page, err := h.orders.ListOrders(c.Request.Context(), filter)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, page)
}

type updateStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

func (h *OrderHandler) updateStatus(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req updateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, bindingError(err))
		return
	}

	order, err := h.orders.UpdateStatus(c.Request.Context(), id, domain.Status(req.Status))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, order)
}

func (h *OrderHandler) delete(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	if err := h.orders.DeleteOrder(c.Request.Context(), id); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// parseIDParam extracts the :id path parameter, responding with a validation
// error and reporting false when it is not a UUID.
func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		v := &domain.ValidationError{}
		v.Add("id", "must be a valid UUID")
		respondError(c, v)
		return uuid.Nil, false
	}
	return id, true
}

// queryInt reads an integer query parameter, falling back when absent or
// unparseable. The service clamps the range, so no bounds check is needed here.
func queryInt(c *gin.Context, key string, fallback int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return fallback
	}
	return v
}

// bindingError turns a binding failure into a domain validation error, so
// malformed JSON and a failed business rule reach the client in one shape.
func bindingError(err error) error {
	v := &domain.ValidationError{}
	v.Add("body", err.Error())
	return v
}
