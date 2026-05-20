package model

// AiChatMessage stores a single chat message in the database.
type AiChatMessage struct {
	Id          uint   `json:"id" gorm:"column:id;type:int(10) unsigned not null AUTO_INCREMENT;primaryKey"`
	CreatedTime int64  `json:"created_time" gorm:"column:created_time;type:int(11);autoCreateTime;index:idx_created_time"`
	SessionId   string `json:"session_id" gorm:"column:session_id;type:varchar(64) not null;default:'';index:idx_session_id"`
	Role        string `json:"role" gorm:"column:role;type:varchar(16) not null;default:''"`
	Content     string `json:"content" gorm:"column:content;type:longtext"`
}
