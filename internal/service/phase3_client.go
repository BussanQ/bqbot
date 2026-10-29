package service

import (
	"bqbot/internal/driver"
	"bqbot/internal/store"
	"net/url"
)

func threadPath(id string) string { return "/v1/threads/" + url.PathEscape(id) }
func roomPath(id string) string   { return "/v1/rooms/" + url.PathEscape(id) }
func (c *Client) Threads(botID string) (v []store.Thread, err error) {
	err = c.do("GET", botPath(botID)+"/threads", nil, &v)
	return
}
func (c *Client) CreateThread(botID, title string) (v store.Thread, err error) {
	err = c.do("POST", botPath(botID)+"/threads", map[string]string{"title": title}, &v)
	return
}
func (c *Client) CloseThread(id string) error {
	return c.do("POST", threadPath(id)+"/close", struct{}{}, nil)
}
func (c *Client) StopThread(id string) error {
	return c.do("POST", threadPath(id)+"/stop", struct{}{}, nil)
}
func (c *Client) ThreadTranscript(id string) (v []store.Message, err error) {
	err = c.do("GET", threadPath(id)+"/transcript", nil, &v)
	return
}
func (c *Client) SendThread(id, text string, attachments []driver.Attachment, mode string) (v store.Message, err error) {
	err = c.do("POST", threadPath(id)+"/send", sendRequest{text, attachments, mode}, &v)
	return
}
func (c *Client) CompactThread(id string) (v store.Thread, err error) {
	err = c.do("POST", threadPath(id)+"/compact", struct{}{}, &v)
	return
}
func (c *Client) Rooms() (v []store.Room, err error) { err = c.do("GET", "/v1/rooms", nil, &v); return }
func (c *Client) SaveRoom(v store.Room) error        { return c.do("PUT", "/v1/rooms", v, nil) }
func (c *Client) DeleteRoom(id string) error         { return c.do("DELETE", roomPath(id), nil, nil) }
func (c *Client) StopRoom(id string) error {
	return c.do("POST", roomPath(id)+"/stop", struct{}{}, nil)
}
func (c *Client) RoomTranscript(id string) (v []store.Message, err error) {
	err = c.do("GET", roomPath(id)+"/transcript", nil, &v)
	return
}
func (c *Client) SendRoom(id, text string, attachments []driver.Attachment) (v store.Message, err error) {
	err = c.do("POST", roomPath(id)+"/send", sendRequest{Text: text, Attachments: attachments}, &v)
	return
}
func (c *Client) CloneBot(id string) (v store.Bot, err error) {
	err = c.do("POST", botPath(id)+"/clone", struct{}{}, &v)
	return
}
func (c *Client) Memories(scopeID string) (v []store.Memory, err error) {
	err = c.do("GET", "/v1/memories/"+url.PathEscape(scopeID), nil, &v)
	return
}
func (c *Client) SaveMemory(scopeID string, v store.Memory) error {
	return c.do("PUT", "/v1/memories/"+url.PathEscape(scopeID), v, nil)
}
func (c *Client) DeleteMemory(scopeID, id string) error {
	return c.do("DELETE", "/v1/memories/"+url.PathEscape(scopeID)+"/"+url.PathEscape(id), nil, nil)
}
func (c *Client) Search(query string) (v []store.SearchResult, err error) {
	err = c.do("GET", "/v1/search?q="+url.QueryEscape(query), nil, &v)
	return
}
func (c *Client) DecisionSettings() (v store.DecisionSettings, err error) {
	err = c.do("GET", "/v1/decision-settings", nil, &v)
	return
}
func (c *Client) SaveDecisionSettings(v store.DecisionSettings) error {
	return c.do("PUT", "/v1/decision-settings", v, nil)
}
