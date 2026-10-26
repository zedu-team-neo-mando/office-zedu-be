package router

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/pkg/controller/office"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/utility"
)

// Office registers the 3D office routes.
func Office(r *gin.Engine, ApiVersion string, validator *validator.Validate, db *storage.Database, logger *utility.Logger) *gin.Engine {
	extReq := request.ExternalRequest{Logger: logger, Test: false}
	officeController := office.Controller{Db: db, Validator: validator, Logger: logger, ExtReq: extReq}

	officeUrl := r.Group(
		fmt.Sprintf("%v/office", ApiVersion),
		middleware.Authorize(db.Postgresql),
		middleware.CheckIsDeactivated(db.Postgresql),
	)

	userUrl := r.Group(
		fmt.Sprintf("%v/users/me", ApiVersion),
		middleware.Authorize(db.Postgresql),
		middleware.CheckIsDeactivated(db.Postgresql),
	)

	{
		// Building and rooms
		officeUrl.GET("/status", officeController.Status)
		officeUrl.GET("/settings", officeController.GetSettings)
		officeUrl.PUT("/settings", officeController.UpdateSettings)
		officeUrl.GET("/bootstrap", officeController.Bootstrap)
		officeUrl.GET("/rooms/:roomId", officeController.GetRoom)
		officeUrl.GET("/rooms/:roomId/occupants", officeController.Occupants)
		officeUrl.POST("/rooms/:roomId/enter", officeController.EnterRoom)
		officeUrl.POST("/rooms/:roomId/leave", officeController.LeaveRoom)

		// Layouts
		officeUrl.GET("/rooms/:roomId/layout", officeController.GetLayout)
		officeUrl.GET("/rooms/:roomId/layout/draft", officeController.GetDraft)
		officeUrl.POST("/rooms/:roomId/layout/draft", officeController.SaveDraft)
		officeUrl.POST("/rooms/:roomId/layout/publish", officeController.PublishLayout)
		officeUrl.POST("/rooms/:roomId/layout/reset", officeController.ResetLayout)
		officeUrl.PUT("/rooms/:roomId/layout", officeController.SavePersonalLayout)

		// Avatar
		userUrl.GET("/avatar", officeController.GetAvatar)
		userUrl.PUT("/avatar", officeController.SaveAvatar)
	}

	return r
}
