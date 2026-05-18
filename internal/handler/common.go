package handler

import (
	"fmt"
	"net/http"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func (s *Server) health(c *gin.Context) {
	response.OK(c, gin.H{"status": "ok"})
}

type authRequest struct {
	Phone    string     `json:"phone" binding:"required"`
	Password string     `json:"password" binding:"required,min=6"`
	Nickname string     `json:"nickname"`
	Role     model.Role `json:"role"`
}

func (s *Server) register(c *gin.Context) {
	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if req.Role == "" {
		req.Role = model.RoleUser
	}
	if req.Role != model.RoleUser && req.Role != model.RoleCreator && req.Role != model.RoleAdmin {
		response.Error(c, http.StatusBadRequest, "invalid role")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to hash password")
		return
	}

	user := model.User{Phone: req.Phone, PasswordHash: string(hash), Nickname: req.Nickname, Role: req.Role}
	if user.Nickname == "" {
		user.Nickname = req.Phone
	}
	if err := s.db.Create(&user).Error; err != nil {
		response.Error(c, http.StatusConflict, "phone already registered")
		return
	}
	response.Created(c, s.authPayload(user))
}

func (s *Server) login(c *gin.Context) {
	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	var user model.User
	if err := s.db.Where("phone = ?", req.Phone).First(&user).Error; err != nil {
		response.Error(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		response.Error(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	response.OK(c, s.authPayload(user))
}

func (s *Server) me(c *gin.Context) {
	var user model.User
	if err := s.db.First(&user, middleware.UserID(c)).Error; err != nil {
		response.Error(c, http.StatusNotFound, "user not found")
		return
	}
	response.OK(c, user)
}

func (s *Server) authPayload(user model.User) gin.H {
	expiresAt := time.Now().Add(s.cfg.JWTExpires)
	claims := middleware.Claims{
		UserID: user.ID,
		Role:   user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte(s.cfg.JWTSecret))
	return gin.H{"token": tokenString, "expires_at": expiresAt, "user": user}
}

func paginate(c *gin.Context) (int, int) {
	page := 1
	pageSize := 20
	if v := c.Query("page"); v != "" {
		if _, err := fmtSscanf(v, &page); err != nil || page < 1 {
			page = 1
		}
	}
	if v := c.Query("page_size"); v != "" {
		if _, err := fmtSscanf(v, &pageSize); err != nil || pageSize < 1 || pageSize > 100 {
			pageSize = 20
		}
	}
	return page, pageSize
}

func fmtSscanf(value string, target *int) (int, error) {
	return fmt.Sscanf(value, "%d", target)
}

func isNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
