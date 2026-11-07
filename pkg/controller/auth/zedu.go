package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/auth"
	telexaudit "github.com/hngprojects/telex_be/services/telexAudit"
	"github.com/hngprojects/telex_be/utility"
)

func (base *Controller) ZeduLogin(c *gin.Context) {
	var req models.ZeduLoginRequestModel

	if err := c.ShouldBindJSON(&req); err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "Failed to parse request body", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	if err := base.Validator.Struct(&req); err != nil {
		rd := utility.BuildErrorResponse(http.StatusUnprocessableEntity, "error", "Validation failed",
			utility.ValidationResponse(err, base.Validator), nil)
		c.JSON(http.StatusUnprocessableEntity, rd)
		return
	}

	respData, code, err := auth.ZeduLogin(c, req, auth.ZeduLoginDeps{
		Db:     base.Db.Postgresql,
		ExtReq: base.ExtReq,
		Logger: base.Logger,
	})
	if err != nil {
		rd := utility.BuildErrorResponse(code, "error", err.Error(), err, nil)
		c.JSON(code, rd)
		return
	}

	if err := telexaudit.LoginAudit(base.Db, base.Logger, respData); err != nil {
		base.Logger.Error("error publishing login audit: ", err.Error())
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "user sign in successfully", respData)
	c.JSON(http.StatusOK, rd)
}
