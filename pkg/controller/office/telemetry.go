package office

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hngprojects/telex_be/internal/models"
	service "github.com/hngprojects/telex_be/services/office"
	"github.com/hngprojects/telex_be/utility"
)

// Telemetry accepts a batch of office events from the requester's own session
// and stores it. It answers 202 Accepted: the batch is queued into the tables
// in one transaction, and the counts in the body tell the client how many rows
// were new, how many were repeats of an earlier batch and how many used an
// event kind this server does not know yet.
//
// The organisation and the user come from the token claims. Anything the body
// claims about who sent it is ignored, so a client cannot write events against
// another organisation. The batch is checked by the service rather than by
// struct tags, because the rules are about relationships between fields, such
// as an offset that must sit inside its own session window.
//
// Bodies are capped at 32 KB and batches at 50 events. A client that sends
// faster than one batch every two seconds gets 429 with no Retry-After
// guesswork: the collector is meant to back off and keep queueing.
func (base *Controller) Telemetry(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	if c.Request.ContentLength > service.MaxTelemetryBodyBytes {
		base.rejectOversizedTelemetry(c)
		return
	}

	// The header can lie, so the reader enforces the same ceiling again while
	// the body is decoded.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxTelemetryBodyBytes)

	var payload models.OfficeTelemetryRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			base.rejectOversizedTelemetry(c)
			return
		}

		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "failed to parse request body", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	response, err := base.service().IngestTelemetry(models.OfficeTelemetryCommand{
		OrganisationID: requester.OrgID,
		UserID:         requester.UserID,
		Request:        payload,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTelemetryRateLimited):
			rd := utility.BuildErrorResponse(http.StatusTooManyRequests, "telemetry_rate_limited", "telemetry batches are arriving too quickly", nil, nil)
			c.JSON(http.StatusTooManyRequests, rd)
		case service.IsTelemetryValidationError(err):
			rd := utility.BuildErrorResponse(http.StatusUnprocessableEntity, "telemetry_invalid", err.Error(), nil, nil)
			c.JSON(http.StatusUnprocessableEntity, rd)
		default:
			base.fail(c, "failed to record the office telemetry", err)
		}
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusAccepted, "telemetry accepted", response)
	c.JSON(http.StatusAccepted, rd)
}

// rejectOversizedTelemetry answers 413 for a batch above the ingest ceiling.
func (base *Controller) rejectOversizedTelemetry(c *gin.Context) {
	rd := utility.BuildErrorResponse(http.StatusRequestEntityTooLarge, "telemetry_too_large", "the telemetry batch is larger than 32 KB", nil, nil)
	c.JSON(http.StatusRequestEntityTooLarge, rd)
}
