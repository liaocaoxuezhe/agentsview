package parser

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openCodeV2Schema mirrors the opencode-next.db layout: the conversation
// lives in session_message.data, while the v1 message/part tables remain
// present but empty. openCodeUsesV2Schema must route such a DB to the v2
// parser even though the v1 tables exist.
const openCodeV2Schema = `
CREATE TABLE project (
	id TEXT PRIMARY KEY,
	worktree TEXT NOT NULL,
	time_created INTEGER NOT NULL DEFAULT 0,
	time_updated INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE session (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	parent_id TEXT,
	title TEXT,
	time_created INTEGER NOT NULL,
	time_updated INTEGER NOT NULL,
	FOREIGN KEY (project_id) REFERENCES project(id)
);

CREATE TABLE message (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL,
	time_updated INTEGER NOT NULL,
	data TEXT NOT NULL,
	FOREIGN KEY (session_id) REFERENCES session(id)
);

CREATE TABLE part (
	id TEXT PRIMARY KEY,
	message_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL,
	time_updated INTEGER NOT NULL,
	data TEXT NOT NULL,
	FOREIGN KEY (message_id) REFERENCES message(id)
);

CREATE TABLE session_message (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	type TEXT NOT NULL,
	seq INTEGER NOT NULL,
	time_created INTEGER NOT NULL,
	time_updated INTEGER NOT NULL,
	data TEXT NOT NULL,
	FOREIGN KEY (session_id) REFERENCES session(id)
);
`

func newV2TestDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "opencode-next.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err, "open v2 test db")
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(openCodeV2Schema)
	require.NoError(t, err, "create v2 schema")
	return dbPath, db
}

func seedV2Session(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO project (id, worktree) VALUES (?, ?)`,
		"prj_v2", "/home/user/code/v2app",
	)
	require.NoError(t, err, "add v2 project")
	_, err = db.Exec(
		`INSERT INTO session
			(id, project_id, parent_id, title, time_created, time_updated)
		 VALUES (?, ?, NULL, ?, ?, ?)`,
		"ses_v2", "prj_v2", "V2 Session", 1700000000000, 1700000060000,
	)
	require.NoError(t, err, "add v2 session")

	userMsg := `{"time":{"created":1700000000000},"text":"帮我写一个 Go 函数","files":[],"agents":[]}`
	assistantMsg := `{"time":{"created":1700000010000,"completed":1700000015000},` +
		`"agent":"build","model":{"id":"deepseek-v4-flash","providerID":"deepseek","variant":"high"},` +
		`"content":[{"type":"reasoning","text":"Let me think about it."},` +
		`{"type":"tool","id":"call_123","name":"execute","executed":false,` +
		`"state":{"status":"completed","input":{"code":"echo hi"}}},` +
		`{"type":"text","text":"Done."}],` +
		`"finish":"done","cost":0.01,` +
		`"tokens":{"input":100,"output":50,"reasoning":20,"cache":{"read":500,"write":0}}}`

	for i, m := range []struct {
		id    string
		mtype string
		data  string
	}{
		{"sm_1", "user", userMsg},
		{"sm_2", "assistant", assistantMsg},
	} {
		_, err = db.Exec(
			`INSERT INTO session_message
				(id, session_id, type, seq, time_created, time_updated, data)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.id, "ses_v2", m.mtype, i+1,
			1700000000000+int64(i*10000),
			1700000000000+int64(i*10000),
			m.data,
		)
		require.NoError(t, err, "add v2 message %s", m.id)
	}
}

func TestOpenCodeUsesV2Schema(t *testing.T) {
	_, db := newV2TestDB(t)
	require.True(t, openCodeUsesV2Schema(db),
		"v2 DB with session_message.data must be detected")

	// A v1 DB without the session_message table must stay on the v1 path.
	v1Path, _, _ := newTestDB(t)
	v1DB, err := sql.Open("sqlite3", v1Path)
	require.NoError(t, err, "open v1 db")
	defer v1DB.Close()
	require.False(t, openCodeUsesV2Schema(v1DB),
		"v1 DB must not be detected as v2")

	// An upgraded v1 DB can carry an empty v2 session_message table (created
	// by the v2 writer) while its real conversation still lives in message/
	// part. Row presence, not schema shape, must keep it on the v1 path.
	_, hybridDB := newV2TestDB(t)
	_, err = hybridDB.Exec(
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
		 VALUES ('msg_v1', 'ses_x', 1, 1, '{}')`,
	)
	require.NoError(t, err, "seed v1-style message row")
	require.False(t, openCodeUsesV2Schema(hybridDB),
		"DB with message rows must be treated as v1 even when session_message exists")
}

func TestParseOpenCodeDB_V2Session(t *testing.T) {
	dbPath, db := newV2TestDB(t)
	seedV2Session(t, db)

	sess, msgs, err := parseOpenCodeDBSession(dbPath, "ses_v2", "host")
	require.NoError(t, err, "parse v2 session")
	require.NotNil(t, sess, "session must not be nil")

	assert.Equal(t, "opencode:ses_v2", sess.ID)
	assert.Equal(t, AgentOpenCode, sess.Agent)
	assert.Equal(t, "v2app", sess.Project)
	assert.Equal(t, "/home/user/code/v2app", sess.Cwd)
	assert.Equal(t, "V2 Session", sess.FirstMessage)
	assert.Equal(t, 2, sess.MessageCount)
	assert.Equal(t, 1, sess.UserMessageCount)
	assert.True(t, sess.File.Path == dbPath+"#ses_v2",
		"virtual path should embed the alt DB name")

	require.Len(t, msgs, 2, "two parsed messages")

	// User message comes from data.text.
	userMsg := msgs[0]
	assert.Equal(t, RoleUser, userMsg.Role)
	assert.Equal(t, "帮我写一个 Go 函数", userMsg.Content)
	assert.False(t, userMsg.HasThinking)
	assert.False(t, userMsg.HasToolUse)

	// Assistant message comes from data.content[] with model/tokens.
	astMsg := msgs[1]
	assert.Equal(t, RoleAssistant, astMsg.Role)
	assert.True(t, astMsg.HasThinking, "reasoning item must set HasThinking")
	assert.True(t, astMsg.HasToolUse, "tool item must set HasToolUse")
	assert.Contains(t, astMsg.Content, "Done.")
	assert.Contains(t, astMsg.Content, "[Thinking]\nLet me think about it.\n[/Thinking]")

	require.Len(t, astMsg.ToolCalls, 1, "one tool call")
	tc := astMsg.ToolCalls[0]
	assert.Equal(t, "execute", tc.ToolName)
	assert.Equal(t, "call_123", tc.ToolUseID)
	assert.JSONEq(t, `{"code":"echo hi"}`, tc.InputJSON,
		"v2 inline state.input must be preserved")

	assert.Equal(t, "deepseek-v4-flash", astMsg.Model,
		"model must come from data.model.id")
	require.NotNil(t, astMsg.TokenUsage, "assistant must carry token usage")
	assert.Equal(t, 50, astMsg.OutputTokens)
	assert.True(t, astMsg.HasOutputTokens)
	assert.Equal(t, 600, astMsg.ContextTokens,
		"context = input 100 + cache.read 500")
	assert.True(t, astMsg.HasContextTokens)

	var tu map[string]int
	require.NoError(t, json.Unmarshal(astMsg.TokenUsage, &tu))
	assert.Equal(t, 100, tu["input_tokens"])
	assert.Equal(t, 50, tu["output_tokens"])
	assert.Equal(t, 500, tu["cache_read_input_tokens"])
	assert.Equal(t, 0, tu["cache_creation_input_tokens"])
}

func TestParseOpenCodeDB_V2SkillTool(t *testing.T) {
	dbPath, db := newV2TestDB(t)
	_, err := db.Exec(
		`INSERT INTO project (id, worktree) VALUES (?, ?)`,
		"prj_v2", "/home/user/code/v2app",
	)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO session
			(id, project_id, parent_id, title, time_created, time_updated)
		 VALUES (?, ?, NULL, NULL, ?, ?)`,
		"ses_v2s", "prj_v2", 1700000000000, 1700000010000,
	)
	require.NoError(t, err)

	skillMsg := `{"time":{"created":1700000005000},` +
		`"model":{"id":"claude-sonnet-4-5","providerID":"anthropic"},` +
		`"content":[{"type":"tool","id":"call_99","name":"skill","executed":false,` +
		`"state":{"status":"completed","input":{"skill":"web-scrape","name":"web-scrape"}}},` +
		`{"type":"text","text":"Using the skill now."}]}`
	_, err = db.Exec(
		`INSERT INTO session_message
			(id, session_id, type, seq, time_created, time_updated, data)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"sm_s1", "ses_v2s", "user", 1, 1700000000000, 1700000000000,
		`{"time":{"created":1700000000000},"text":"use skill"}`,
	)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO session_message
			(id, session_id, type, seq, time_created, time_updated, data)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"sm_s2", "ses_v2s", "assistant", 2, 1700000005000, 1700000009000,
		skillMsg,
	)
	require.NoError(t, err)

	sess, msgs, err := parseOpenCodeDBSession(dbPath, "ses_v2s", "host")
	require.NoError(t, err, "parse v2 skill session")
	require.NotNil(t, sess)
	require.Len(t, msgs, 2, "two messages")

	astMsg := msgs[1]
	require.Len(t, astMsg.ToolCalls, 1)
	tc := astMsg.ToolCalls[0]
	assert.Equal(t, "skill", tc.ToolName)
	assert.Equal(t, "web-scrape", tc.SkillName)
	assert.Equal(t, "claude-sonnet-4-5", astMsg.Model)
}

func TestListOpenCodeSessionMeta_V2DB(t *testing.T) {
	dbPath, db := newV2TestDB(t)
	seedV2Session(t, db)

	metas, err := ListOpenCodeSessionMeta(dbPath)
	require.NoError(t, err, "list v2 session meta")
	require.Len(t, metas, 1)
	assert.Equal(t, "ses_v2", metas[0].SessionID)
	assert.Equal(t, dbPath+"#ses_v2", metas[0].VirtualPath)
}
