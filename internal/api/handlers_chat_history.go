package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/model"
)

// ListChatSessions 获取聊天会话列表（按更新时间倒序，最近 50 条）
func ListChatSessions(c *gin.Context) {
	db := core.GetDB()
	var sessions []model.ChatSession
	if err := db.Order("updated_at DESC").Limit(50).Find(&sessions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	if sessions == nil {
		sessions = []model.ChatSession{}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    sessions,
	})
}

// CreateChatSession 创建新会话
func CreateChatSession(c *gin.Context) {
	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		// 允许空 body，title 默认为 "新对话"
		body.Title = ""
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "新对话"
	}

	db := core.GetDB()
	session := model.ChatSession{Title: title}
	if err := db.Create(&session).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    session,
	})
}

// GetChatSession 获取会话详情（含全部消息）
func GetChatSession(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "无效的会话 ID",
		})
		return
	}

	db := core.GetDB()
	var session model.ChatSession
	if err := db.First(&session, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   "会话不存在",
		})
		return
	}

	var messages []model.ChatMessageRecord
	if err := db.Where("session_id = ?", id).Order("id ASC").Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	if messages == nil {
		messages = []model.ChatMessageRecord{}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"session":  session,
			"messages": messages,
		},
	})
}

// UpdateChatSession 更新会话标题
func UpdateChatSession(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "无效的会话 ID",
		})
		return
	}

	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	title := strings.TrimSpace(body.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "标题不能为空",
		})
		return
	}

	db := core.GetDB()
	if err := db.Model(&model.ChatSession{}).Where("id = ?", id).Update("title", title).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"id": id, "title": title},
	})
}

// SaveChatMessage 保存一条消息到指定会话（供流式完成后调用）
func SaveChatMessage(c *gin.Context) {
	idStr := c.Param("id")
	sessionID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "无效的会话 ID",
		})
		return
	}

	var body struct {
		Role    string `json:"role" binding:"required"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	db := core.GetDB()

	// 检查会话是否存在
	var session model.ChatSession
	if err := db.First(&session, sessionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   "会话不存在",
		})
		return
	}

	msg := model.ChatMessageRecord{
		SessionID: uint(sessionID),
		Role:      body.Role,
		Content:   body.Content,
	}
	if err := db.Create(&msg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 更新会话的 updated_at
	db.Model(&session).Update("updated_at", nil)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    msg,
	})
}

// DeleteChatSession 删除会话及其所有消息
func DeleteChatSession(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "无效的会话 ID",
		})
		return
	}

	db := core.GetDB()

	// 级联删除消息
	if err := db.Where("session_id = ?", id).Delete(&model.ChatMessageRecord{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	if err := db.Delete(&model.ChatSession{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"id": id},
	})
}
