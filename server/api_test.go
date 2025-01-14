package chserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/cloudradar-monitoring/rport/server/api"
	"github.com/cloudradar-monitoring/rport/server/api/jobs"
	"github.com/cloudradar-monitoring/rport/server/api/users"
	"github.com/cloudradar-monitoring/rport/server/cgroups"
	"github.com/cloudradar-monitoring/rport/server/clients"
	"github.com/cloudradar-monitoring/rport/server/clientsauth"
	"github.com/cloudradar-monitoring/rport/server/test/jb"
	chshare "github.com/cloudradar-monitoring/rport/share"
	"github.com/cloudradar-monitoring/rport/share/comm"
	"github.com/cloudradar-monitoring/rport/share/models"
	"github.com/cloudradar-monitoring/rport/share/ptr"
	"github.com/cloudradar-monitoring/rport/share/random"
	"github.com/cloudradar-monitoring/rport/share/security"
	"github.com/cloudradar-monitoring/rport/share/test"
)

var testLog = chshare.NewLogger("api-listener-test", chshare.LogOutput{File: os.Stdout}, chshare.LogLevelDebug)
var hour = time.Hour

type JobProviderMock struct {
	JobProvider
	ReturnJob          *models.Job
	ReturnJobSummaries []*models.JobSummary
	ReturnErr          error

	InputCID       string
	InputJID       string
	InputSaveJob   *models.Job
	InputCreateJob *models.Job
}

func NewJobProviderMock() *JobProviderMock {
	return &JobProviderMock{}
}

func (p *JobProviderMock) GetByJID(cid, jid string) (*models.Job, error) {
	p.InputCID = cid
	p.InputJID = jid
	return p.ReturnJob, p.ReturnErr
}

func (p *JobProviderMock) GetSummariesByClientID(cid string) ([]*models.JobSummary, error) {
	p.InputCID = cid
	return p.ReturnJobSummaries, p.ReturnErr
}

func (p *JobProviderMock) SaveJob(job *models.Job) error {
	p.InputSaveJob = job
	return p.ReturnErr
}

func (p *JobProviderMock) CreateJob(job *models.Job) error {
	p.InputCreateJob = job
	return p.ReturnErr
}

func (p *JobProviderMock) Close() error {
	return nil
}

func TestGetCorrespondingSortFuncPositive(t *testing.T) {
	testCases := []struct {
		sortStr string

		wantFunc func(a []*clients.Client, desc bool)
		wantDesc bool
	}{
		{
			sortStr:  "",
			wantFunc: clients.SortByID,
			wantDesc: false,
		},
		{
			sortStr:  "-",
			wantFunc: clients.SortByID,
			wantDesc: true,
		},
		{
			sortStr:  "id",
			wantFunc: clients.SortByID,
			wantDesc: false,
		},
		{
			sortStr:  "-id",
			wantFunc: clients.SortByID,
			wantDesc: true,
		},
		{
			sortStr:  "name",
			wantFunc: clients.SortByName,
			wantDesc: false,
		},
		{
			sortStr:  "-name",
			wantFunc: clients.SortByName,
			wantDesc: true,
		},
		{
			sortStr:  "hostname",
			wantFunc: clients.SortByHostname,
			wantDesc: false,
		},
		{
			sortStr:  "-hostname",
			wantFunc: clients.SortByHostname,
			wantDesc: true,
		},
		{
			sortStr:  "os",
			wantFunc: clients.SortByOS,
			wantDesc: false,
		},
		{
			sortStr:  "-os",
			wantFunc: clients.SortByOS,
			wantDesc: true,
		},
	}

	for _, tc := range testCases {
		// when
		gotFunc, gotDesc, gotErr := getCorrespondingSortFunc(tc.sortStr)

		// then
		// workaround to compare func vars, see https://github.com/stretchr/testify/issues/182
		wantFuncName := runtime.FuncForPC(reflect.ValueOf(tc.wantFunc).Pointer()).Name()
		gotFuncName := runtime.FuncForPC(reflect.ValueOf(gotFunc).Pointer()).Name()
		msg := fmt.Sprintf("getCorrespondingSortFunc(%q) = (%s, %v, %v), expected: (%s, %v, %v)", tc.sortStr, gotFuncName, gotDesc, gotErr, wantFuncName, tc.wantDesc, nil)

		assert.NoErrorf(t, gotErr, msg)
		assert.Equalf(t, wantFuncName, gotFuncName, msg)
		assert.Equalf(t, tc.wantDesc, gotDesc, msg)
	}
}

func TestGetCorrespondingSortFuncNegative(t *testing.T) {
	// when
	_, _, gotErr := getCorrespondingSortFunc("unknown")

	// then
	require.Error(t, gotErr)
	assert.Contains(t, gotErr.Error(), "incorrect format")
}

var (
	cl1 = &clientsauth.ClientAuth{ID: "user1", Password: "pswd1"}
	cl2 = &clientsauth.ClientAuth{ID: "user2", Password: "pswd2"}
	cl3 = &clientsauth.ClientAuth{ID: "user3", Password: "pswd3"}
)

func TestHandleGetClientsAuth(t *testing.T) {
	require := require.New(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients-auth", nil)

	testCases := []struct {
		descr string // Test Case Description

		provider clientsauth.Provider

		wantStatusCode  int
		wantClientsAuth []*clientsauth.ClientAuth
		wantErrCode     string
		wantErrTitle    string
	}{
		{
			descr:           "auth file, 3 clients",
			provider:        clientsauth.NewMockProvider([]*clientsauth.ClientAuth{cl1, cl2, cl3}),
			wantStatusCode:  http.StatusOK,
			wantClientsAuth: []*clientsauth.ClientAuth{cl1, cl2, cl3},
		},
		{
			descr:           "auth file, no clients",
			provider:        clientsauth.NewMockProvider([]*clientsauth.ClientAuth{}),
			wantStatusCode:  http.StatusOK,
			wantClientsAuth: []*clientsauth.ClientAuth{},
		},
		{
			descr:           "auth, single client",
			provider:        clientsauth.NewSingleProvider(cl1.ID, cl1.Password),
			wantStatusCode:  http.StatusOK,
			wantClientsAuth: []*clientsauth.ClientAuth{cl1},
		},
	}

	for _, tc := range testCases {
		msg := fmt.Sprintf("test case: %q", tc.descr)

		// given
		al := APIListener{
			Logger: testLog,
			Server: &Server{
				config: &Config{
					Server: ServerConfig{MaxRequestBytes: 1024 * 1024},
				},
				clientAuthProvider: tc.provider,
			},
		}

		// when
		handler := http.HandlerFunc(al.handleGetClientsAuth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		// then
		require.Equalf(tc.wantStatusCode, w.Code, msg)
		var wantResp interface{}
		if tc.wantErrTitle == "" {
			// success case
			wantResp = api.NewSuccessPayload(tc.wantClientsAuth)
		} else {
			// failure case
			wantResp = api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, "")
		}
		wantRespBytes, err := json.Marshal(wantResp)
		require.NoErrorf(err, msg)
		require.Equalf(string(wantRespBytes), w.Body.String(), msg)
	}
}

func TestHandlePostClients(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	composeRequestBody := func(id, pswd string) io.Reader {
		c := clientsauth.ClientAuth{ID: id, Password: pswd}
		b, err := json.Marshal(c)
		require.NoError(err)
		return bytes.NewBuffer(b)
	}
	cl4 := &clientsauth.ClientAuth{ID: "user4", Password: "pswd4"}
	initCacheState := []*clientsauth.ClientAuth{cl1, cl2, cl3}

	testCases := []struct {
		descr string // Test Case Description

		provider        clientsauth.Provider
		clientAuthWrite bool
		requestBody     io.Reader

		wantStatusCode  int
		wantClientsAuth []*clientsauth.ClientAuth
		wantErrCode     string
		wantErrTitle    string
		wantErrDetail   string
	}{
		{
			descr:           "auth file, new valid client",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     composeRequestBody(cl4.ID, cl4.Password),
			wantStatusCode:  http.StatusCreated,
			wantClientsAuth: []*clientsauth.ClientAuth{cl1, cl2, cl3, cl4},
		},
		{
			descr:           "auth file, new valid client, empty cache",
			provider:        clientsauth.NewMockProvider([]*clientsauth.ClientAuth{}),
			clientAuthWrite: true,
			requestBody:     composeRequestBody(cl4.ID, cl4.Password),
			wantStatusCode:  http.StatusCreated,
			wantClientsAuth: []*clientsauth.ClientAuth{cl4},
		},
		{
			descr:           "auth file, empty request body",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     strings.NewReader(""),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     "",
			wantErrTitle:    "Missing body with json data.",
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request body",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     strings.NewReader("invalid json"),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     "",
			wantErrTitle:    "Invalid JSON data.",
			wantErrDetail:   "invalid character 'i' looking for beginning of value",
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request, empty id",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     composeRequestBody("", cl4.Password),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid or missing ID.",
			wantErrDetail:   fmt.Sprintf("Min size is %d.", MinCredentialsLength),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request, 'id' is missing",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     strings.NewReader(`{"password":"pswd"}`),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid or missing ID.",
			wantErrDetail:   fmt.Sprintf("Min size is %d.", MinCredentialsLength),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request, empty password",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     composeRequestBody(cl4.ID, ""),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid or missing password.",
			wantErrDetail:   fmt.Sprintf("Min size is %d.", MinCredentialsLength),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request, 'password' is missing",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     strings.NewReader(`{"id":"user"}`),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid or missing password.",
			wantErrDetail:   fmt.Sprintf("Min size is %d.", MinCredentialsLength),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request, id too short",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     composeRequestBody("12", cl4.Password),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid or missing ID.",
			wantErrDetail:   fmt.Sprintf("Min size is %d.", MinCredentialsLength),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, invalid request, password too short",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     composeRequestBody(cl4.ID, "12"),
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid or missing password.",
			wantErrDetail:   fmt.Sprintf("Min size is %d.", MinCredentialsLength),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, client already exist",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			requestBody:     composeRequestBody(cl1.ID, cl4.Password),
			wantStatusCode:  http.StatusConflict,
			wantErrCode:     ErrCodeAlreadyExist,
			wantErrTitle:    fmt.Sprintf("Client Auth with ID %q already exist.", cl1.ID),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, auth in Read-Only mode",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: false,
			requestBody:     composeRequestBody(cl1.ID, cl4.Password),
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantErrCode:     ErrCodeClientAuthRO,
			wantErrTitle:    "Client authentication has been attached in read-only mode.",
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth, single client",
			provider:        clientsauth.NewSingleProvider(cl1.ID, cl1.Password),
			clientAuthWrite: true,
			requestBody:     composeRequestBody(cl4.ID, cl4.Password),
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantErrCode:     ErrCodeClientAuthSingleClient,
			wantErrTitle:    "Client authentication is enabled only for a single user.",
			wantClientsAuth: []*clientsauth.ClientAuth{cl1},
		},
	}

	for _, tc := range testCases {
		msg := fmt.Sprintf("test case: %q", tc.descr)

		// given
		al := APIListener{
			Server: &Server{
				config: &Config{
					Server: ServerConfig{
						AuthWrite:       tc.clientAuthWrite,
						MaxRequestBytes: 1024 * 1024,
					},
				},
				clientAuthProvider: tc.provider,
			},
			Logger: testLog,
		}

		req := httptest.NewRequest(http.MethodPost, "/api/v1/clients-auth", tc.requestBody)

		// when
		handler := http.HandlerFunc(al.handlePostClientsAuth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		// then
		require.Equalf(tc.wantStatusCode, w.Code, msg)
		if tc.wantErrTitle == "" {
			// success case
			assert.Emptyf(w.Body.String(), msg)
		} else {
			// failure case
			wantResp := api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, tc.wantErrDetail)
			wantRespBytes, err := json.Marshal(wantResp)
			require.NoErrorf(err, msg)
			require.Equalf(string(wantRespBytes), w.Body.String(), msg)
		}
		clients, err := al.clientAuthProvider.GetAll()
		require.NoError(err)
		assert.ElementsMatchf(tc.wantClientsAuth, clients, msg)
	}
}

type mockConnection struct {
	ssh.Conn
	closed bool
}

func (m *mockConnection) Close() error {
	m.closed = true
	return nil
}

func TestHandleDeleteClient(t *testing.T) {
	mockConn := &mockConnection{}

	initCacheState := []*clientsauth.ClientAuth{cl1, cl2, cl3}

	c1 := clients.New(t).ClientAuthID(cl1.ID).Connection(mockConn).Build()
	c2 := clients.New(t).ClientAuthID(cl1.ID).DisconnectedDuration(5 * time.Minute).Build()

	testCases := []struct {
		descr string // Test Case Description

		provider        clientsauth.Provider
		clients         []*clients.Client
		clientAuthWrite bool
		clientAuthID    string
		urlSuffix       string

		wantStatusCode  int
		wantClientsAuth []*clientsauth.ClientAuth
		wantErrCode     string
		wantErrTitle    string
		wantErrDetail   string
		wantClosedConn  bool
		wantClients     []*clients.Client
	}{
		{
			descr:           "auth file, success delete",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			wantStatusCode:  http.StatusNoContent,
			wantClientsAuth: []*clientsauth.ClientAuth{cl2, cl3},
		},
		{
			descr:           "auth file, missing client ID",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: true,
			clientAuthID:    "unknown-client-id",
			wantStatusCode:  http.StatusNotFound,
			wantErrCode:     ErrCodeClientAuthNotFound,
			wantErrTitle:    fmt.Sprintf("Client Auth with ID=%q not found.", "unknown-client-id"),
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, client has active client",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clients:         []*clients.Client{c1},
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			wantStatusCode:  http.StatusConflict,
			wantErrCode:     ErrCodeClientAuthHasClient,
			wantErrTitle:    fmt.Sprintf("Client Auth expected to have no active or disconnected bound client(s), got %d.", 1),
			wantClientsAuth: initCacheState,
			wantClients:     []*clients.Client{c1},
		},
		{
			descr:           "auth file, client auth has disconnected client",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clients:         []*clients.Client{c2},
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			wantStatusCode:  http.StatusConflict,
			wantErrCode:     ErrCodeClientAuthHasClient,
			wantErrTitle:    fmt.Sprintf("Client Auth expected to have no active or disconnected bound client(s), got %d.", 1),
			wantClientsAuth: initCacheState,
			wantClients:     []*clients.Client{c2},
		},
		{
			descr:           "auth file, auth in Read-Only mode",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clientAuthWrite: false,
			clientAuthID:    cl1.ID,
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantErrCode:     ErrCodeClientAuthRO,
			wantErrTitle:    "Client authentication has been attached in read-only mode.",
			wantClientsAuth: initCacheState,
		},
		{
			descr:           "auth file, client auth has active client, force",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clients:         []*clients.Client{c1},
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			urlSuffix:       "?force=true",
			wantStatusCode:  http.StatusNoContent,
			wantClientsAuth: []*clientsauth.ClientAuth{cl2, cl3},
			wantClosedConn:  true,
		},
		{
			descr:           "auth file, client auth has disconnected bound client, force",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clients:         []*clients.Client{c2},
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			urlSuffix:       "?force=true",
			wantStatusCode:  http.StatusNoContent,
			wantClientsAuth: []*clientsauth.ClientAuth{cl2, cl3},
		},
		{
			descr:           "invalid force param",
			provider:        clientsauth.NewMockProvider(initCacheState),
			clients:         []*clients.Client{c1, c2},
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			urlSuffix:       "?force=test",
			wantStatusCode:  http.StatusBadRequest,
			wantErrCode:     ErrCodeInvalidRequest,
			wantErrTitle:    "Invalid force param test.",
			wantClientsAuth: initCacheState,
			wantClients:     []*clients.Client{c1, c2},
		},
		{
			descr:           "auth, single client",
			provider:        clientsauth.NewSingleProvider(cl1.ID, cl1.Password),
			clientAuthWrite: true,
			clientAuthID:    cl1.ID,
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantErrCode:     ErrCodeClientAuthSingleClient,
			wantErrTitle:    "Client authentication is enabled only for a single user.",
			wantClientsAuth: []*clientsauth.ClientAuth{cl1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.descr, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			// given
			al := APIListener{
				insecureForTests: true,
				Server: &Server{
					clientService: NewClientService(nil, clients.NewClientRepository(tc.clients, &hour, testLog)),
					config: &Config{
						Server: ServerConfig{
							AuthWrite:       tc.clientAuthWrite,
							MaxRequestBytes: 1024 * 1024,
						},
					},
					clientAuthProvider: tc.provider,
				},
				Logger: testLog,
			}
			al.initRouter()
			mockConn.closed = false

			url := fmt.Sprintf("/api/v1/clients-auth/%s", tc.clientAuthID)
			url += tc.urlSuffix
			req := httptest.NewRequest(http.MethodDelete, url, nil)

			// when
			w := httptest.NewRecorder()
			al.router.ServeHTTP(w, req)

			// then
			assert.Equal(tc.wantStatusCode, w.Code)
			var wantRespStr string
			if tc.wantErrTitle == "" {
				// success case: empty body
			} else {
				// failure case
				wantResp := api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, tc.wantErrDetail)
				wantRespBytes, err := json.Marshal(wantResp)
				require.NoError(err)
				wantRespStr = string(wantRespBytes)
			}
			assert.Equal(wantRespStr, w.Body.String())
			clients, err := al.clientAuthProvider.GetAll()
			require.NoError(err)
			assert.ElementsMatch(tc.wantClientsAuth, clients)
			assert.Equal(tc.wantClosedConn, mockConn.closed)
			allClients, err := al.clientService.GetAll()
			require.NoError(err)
			assert.ElementsMatch(tc.wantClients, allClients)
		})
	}
}

func TestHandlePostCommand(t *testing.T) {
	var testJID string
	generateNewJobID = func() (string, error) {
		uuid, err := random.UUID4()
		testJID = uuid
		return uuid, err
	}
	testUser := "test-user"

	defaultTimeout := 60
	gotCmd := "/bin/date;foo;whoami"
	gotCmdTimeoutSec := 30
	validReqBody := `{"command": "` + gotCmd + `","timeout_sec": ` + strconv.Itoa(gotCmdTimeoutSec) + `}`

	connMock := test.NewConnMock()
	// by default set to return success
	connMock.ReturnOk = true
	sshSuccessResp := comm.RunCmdResponse{Pid: 123, StartedAt: time.Date(2020, 10, 10, 10, 10, 10, 0, time.UTC)}
	sshRespBytes, err := json.Marshal(sshSuccessResp)
	require.NoError(t, err)
	connMock.ReturnResponsePayload = sshRespBytes

	c1 := clients.New(t).Connection(connMock).Build()
	c2 := clients.New(t).DisconnectedDuration(5 * time.Minute).Build()

	testCases := []struct {
		name string

		cid             string
		requestBody     string
		jpReturnSaveErr error
		connReturnErr   error
		connReturnNotOk bool
		connReturnResp  []byte
		runningJob      *models.Job
		clients         []*clients.Client

		wantStatusCode  int
		wantTimeout     int
		wantErrCode     string
		wantErrTitle    string
		wantErrDetail   string
		wantInterpreter string
	}{
		{
			name:           "valid cmd",
			requestBody:    validReqBody,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusOK,
			wantTimeout:    gotCmdTimeoutSec,
		},
		{
			name:            "valid cmd with interpreter",
			requestBody:     `{"command": "` + gotCmd + `","interpreter": "powershell"}`,
			cid:             c1.ID,
			clients:         []*clients.Client{c1},
			wantStatusCode:  http.StatusOK,
			wantTimeout:     defaultTimeout,
			wantInterpreter: "powershell",
		},
		{
			name:           "invalid interpreter",
			requestBody:    `{"command": "` + gotCmd + `","interpreter": "unsupported"}`,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "Invalid interpreter.",
			wantErrDetail:  "expected interpreter to be one of: [cmd powershell tacoscript], actual: unsupported",
		},
		{
			name:           "valid cmd with no timeout",
			requestBody:    `{"command": "/bin/date;foo;whoami"}`,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantTimeout:    defaultTimeout,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "valid cmd with 0 timeout",
			requestBody:    `{"command": "/bin/date;foo;whoami", "timeout_sec": 0}`,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantTimeout:    defaultTimeout,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "empty cmd",
			requestBody:    `{"command": "", "timeout_sec": 30}`,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "Command cannot be empty.",
		},
		{
			name:           "no cmd",
			requestBody:    `{"timeout_sec": 30}`,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "Command cannot be empty.",
		},
		{
			name:           "empty body",
			requestBody:    "",
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "Missing body with json data.",
		},
		{
			name:           "invalid request body",
			requestBody:    "sdfn fasld fasdf sdlf jd",
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "Invalid JSON data.",
			wantErrDetail:  "invalid character 's' looking for beginning of value",
		},
		{
			name:           "invalid request body: unknown param",
			requestBody:    `{"command": "/bin/date;foo;whoami", "timeout": 30}`,
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "Invalid JSON data.",
			wantErrDetail:  "json: unknown field \"timeout\"",
		},
		{
			name:           "no active client",
			requestBody:    validReqBody,
			cid:            c1.ID,
			clients:        []*clients.Client{},
			wantStatusCode: http.StatusNotFound,
			wantErrTitle:   fmt.Sprintf("Active client with id=%q not found.", c1.ID),
		},
		{
			name:           "disconnected client",
			requestBody:    validReqBody,
			cid:            c2.ID,
			clients:        []*clients.Client{c1, c2},
			wantStatusCode: http.StatusNotFound,
			wantErrTitle:   fmt.Sprintf("Active client with id=%q not found.", c2.ID),
		},
		{
			name:            "error on save job",
			requestBody:     validReqBody,
			jpReturnSaveErr: errors.New("save fake error"),
			cid:             c1.ID,
			clients:         []*clients.Client{c1},
			wantStatusCode:  http.StatusInternalServerError,
			wantErrTitle:    "Failed to persist a new job.",
			wantErrDetail:   "save fake error",
		},
		{
			name:           "error on send request",
			requestBody:    validReqBody,
			connReturnErr:  errors.New("send fake error"),
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusInternalServerError,
			wantErrTitle:   "Failed to execute remote command.",
			wantErrDetail:  "failed to send request: send fake error",
		},
		{
			name:           "invalid ssh response format",
			requestBody:    validReqBody,
			connReturnResp: []byte("invalid ssh response data"),
			cid:            c1.ID,
			clients:        []*clients.Client{c1},
			wantStatusCode: http.StatusConflict,
			wantErrTitle:   "invalid client response format: failed to decode response into *comm.RunCmdResponse: invalid character 'i' looking for beginning of value",
		},
		{
			name:            "failure response on send request",
			requestBody:     validReqBody,
			connReturnNotOk: true,
			connReturnResp:  []byte("fake failure msg"),
			cid:             c1.ID,
			clients:         []*clients.Client{c1},
			wantStatusCode:  http.StatusConflict,
			wantErrTitle:    "client error: fake failure msg",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			al := APIListener{
				insecureForTests: true,
				Server: &Server{
					clientService: NewClientService(nil, clients.NewClientRepository(tc.clients, &hour, testLog)),
					config: &Config{
						Server: ServerConfig{
							RunRemoteCmdTimeoutSec: defaultTimeout,
							MaxRequestBytes:        1024 * 1024,
						},
					},
				},
				Logger: testLog,
			}
			al.initRouter()

			jp := NewJobProviderMock()
			jp.ReturnErr = tc.jpReturnSaveErr
			al.jobProvider = jp

			connMock.ReturnErr = tc.connReturnErr
			connMock.ReturnOk = !tc.connReturnNotOk
			if len(tc.connReturnResp) > 0 {
				connMock.ReturnResponsePayload = tc.connReturnResp // override stubbed success payload
			}

			ctx := api.WithUser(context.Background(), testUser)
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/clients/%s/commands", tc.cid), strings.NewReader(tc.requestBody))
			req = req.WithContext(ctx)

			// when
			w := httptest.NewRecorder()
			al.router.ServeHTTP(w, req)

			// then
			assert.Equal(t, tc.wantStatusCode, w.Code)
			if tc.wantErrTitle == "" {
				// success case
				assert.Equal(t, fmt.Sprintf("{\"data\":{\"jid\":\"%s\"}}", testJID), w.Body.String())
				gotRunningJob := jp.InputCreateJob
				assert.NotNil(t, gotRunningJob)
				assert.Equal(t, testJID, gotRunningJob.JID)
				assert.Equal(t, models.JobStatusRunning, gotRunningJob.Status)
				assert.Nil(t, gotRunningJob.FinishedAt)
				assert.Equal(t, tc.cid, gotRunningJob.ClientID)
				assert.Equal(t, gotCmd, gotRunningJob.Command)
				assert.Equal(t, tc.wantInterpreter, gotRunningJob.Interpreter)
				assert.Equal(t, &sshSuccessResp.Pid, gotRunningJob.PID)
				assert.Equal(t, sshSuccessResp.StartedAt, gotRunningJob.StartedAt)
				assert.Equal(t, testUser, gotRunningJob.CreatedBy)
				assert.Equal(t, tc.wantTimeout, gotRunningJob.TimeoutSec)
				assert.Nil(t, gotRunningJob.Result)
			} else {
				// failure case
				wantResp := api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, tc.wantErrDetail)
				wantRespBytes, err := json.Marshal(wantResp)
				require.NoError(t, err)
				require.Equal(t, string(wantRespBytes), w.Body.String())
			}
		})
	}
}

func TestHandleGetCommand(t *testing.T) {
	wantJob := jb.New(t).ClientID("cid-1234").JID("jid-1234").Build()
	wantJobResp := api.NewSuccessPayload(wantJob)
	b, err := json.Marshal(wantJobResp)
	require.NoError(t, err)
	wantJobRespJSON := string(b)

	testCases := []struct {
		name string

		jpReturnErr error
		jpReturnJob *models.Job

		wantStatusCode int
		wantErrCode    string
		wantErrTitle   string
		wantErrDetail  string
	}{
		{
			name:           "job found",
			jpReturnJob:    wantJob,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "not found",
			jpReturnJob:    nil,
			wantStatusCode: http.StatusNotFound,
			wantErrTitle:   fmt.Sprintf("Job[id=%q] not found.", wantJob.JID),
		},
		{
			name:           "error on get job",
			jpReturnErr:    errors.New("get job fake error"),
			wantStatusCode: http.StatusInternalServerError,
			wantErrTitle:   fmt.Sprintf("Failed to find a job[id=%q].", wantJob.JID),
			wantErrDetail:  "get job fake error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			al := APIListener{
				insecureForTests: true,
				Logger:           testLog,
				Server: &Server{
					config: &Config{
						Server: ServerConfig{MaxRequestBytes: 1024 * 1024},
					},
				},
			}
			al.initRouter()

			jp := NewJobProviderMock()
			jp.ReturnErr = tc.jpReturnErr
			jp.ReturnJob = tc.jpReturnJob
			al.jobProvider = jp

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/clients/%s/commands/%s", wantJob.ClientID, wantJob.JID), nil)

			// when
			w := httptest.NewRecorder()
			al.router.ServeHTTP(w, req)

			// then
			assert.Equal(t, tc.wantStatusCode, w.Code)
			if tc.wantErrTitle == "" {
				// success case
				assert.Equal(t, wantJobRespJSON, w.Body.String())
				assert.Equal(t, wantJob.ClientID, jp.InputCID)
				assert.Equal(t, wantJob.JID, jp.InputJID)
			} else {
				// failure case
				wantResp := api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, tc.wantErrDetail)
				wantRespBytes, err := json.Marshal(wantResp)
				require.NoError(t, err)
				require.Equal(t, string(wantRespBytes), w.Body.String())
			}
		})
	}
}

func TestHandleGetCommands(t *testing.T) {
	ft := time.Date(2020, 10, 10, 10, 10, 10, 0, time.UTC)
	testCID := "cid-1234"
	jb := jb.New(t).ClientID(testCID)
	job1 := jb.Status(models.JobStatusSuccessful).FinishedAt(ft).Build().JobSummary
	job2 := jb.Status(models.JobStatusUnknown).FinishedAt(ft.Add(-time.Hour)).Build().JobSummary
	job3 := jb.Status(models.JobStatusFailed).FinishedAt(ft.Add(time.Minute)).Build().JobSummary
	job4 := jb.Status(models.JobStatusRunning).Build().JobSummary
	jpSuccessReturnJobSummaries := []*models.JobSummary{&job1, &job2, &job3, &job4}
	wantSuccessResp := api.NewSuccessPayload([]*models.JobSummary{&job4, &job3, &job1, &job2}) // sorted in desc
	b, err := json.Marshal(wantSuccessResp)
	require.NoError(t, err)
	wantSuccessRespJobsJSON := string(b)

	testCases := []struct {
		name string

		jpReturnErr          error
		jpReturnJobSummaries []*models.JobSummary

		wantStatusCode  int
		wantSuccessResp string
		wantErrCode     string
		wantErrTitle    string
		wantErrDetail   string
	}{
		{
			name:                 "found few jobs",
			jpReturnJobSummaries: jpSuccessReturnJobSummaries,
			wantSuccessResp:      wantSuccessRespJobsJSON,
			wantStatusCode:       http.StatusOK,
		},
		{
			name:                 "not found",
			jpReturnJobSummaries: []*models.JobSummary{},
			wantSuccessResp:      `{"data":[]}`,
			wantStatusCode:       http.StatusOK,
		},
		{
			name:           "error on get job summaries",
			jpReturnErr:    errors.New("get job summaries fake error"),
			wantStatusCode: http.StatusInternalServerError,
			wantErrTitle:   fmt.Sprintf("Failed to get client jobs: client_id=%q.", testCID),
			wantErrDetail:  "get job summaries fake error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			al := APIListener{
				insecureForTests: true,
				Logger:           testLog,
				Server: &Server{
					config: &Config{
						Server: ServerConfig{MaxRequestBytes: 1024 * 1024},
					},
				},
			}
			al.initRouter()

			jp := NewJobProviderMock()
			jp.ReturnErr = tc.jpReturnErr
			jp.ReturnJobSummaries = tc.jpReturnJobSummaries
			al.jobProvider = jp

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/clients/%s/commands", testCID), nil)

			// when
			w := httptest.NewRecorder()
			al.router.ServeHTTP(w, req)

			// then
			assert.Equal(t, tc.wantStatusCode, w.Code)
			if tc.wantErrTitle == "" {
				// success case
				assert.Equal(t, tc.wantSuccessResp, w.Body.String())
				assert.Equal(t, testCID, jp.InputCID)
			} else {
				// failure case
				wantResp := api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, tc.wantErrDetail)
				wantRespBytes, err := json.Marshal(wantResp)
				require.NoError(t, err)
				require.Equal(t, string(wantRespBytes), w.Body.String())
			}
		})
	}
}

func TestHandleGetClients(t *testing.T) {
	curUser := &users.User{
		Username: "admin",
		Groups:   []string{users.Administrators},
	}
	c1 := clients.New(t).ID("client-1").ClientAuthID(cl1.ID).Build()
	c2 := clients.New(t).ID("client-2").ClientAuthID(cl1.ID).DisconnectedDuration(5 * time.Minute).Build()
	al := APIListener{
		insecureForTests: true,
		Server: &Server{
			clientService: NewClientService(nil, clients.NewClientRepository([]*clients.Client{c1, c2}, &hour, testLog)),
			config: &Config{
				Server: ServerConfig{MaxRequestBytes: 1024 * 1024},
			},
		},
		userService: users.NewAPIService(users.NewStaticProvider([]*users.User{curUser}), false),
	}
	al.initRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/clients", nil)
	ctx := api.WithUser(context.Background(), curUser.Username)
	req = req.WithContext(ctx)
	al.router.ServeHTTP(w, req)

	expectedJSON := `{
   "data":[
      {
         "id":"client-1",
         "mem_total":100000,
         "name":"Random Rport Client",
         "num_cpus":2,
         "os":"Linux alpine-3-10-tk-01 4.19.80-0-virt #1-Alpine SMP Fri Oct 18 11:51:24 UTC 2019 x86_64 Linux",
         "os_arch":"amd64",
         "os_family":"alpine",
         "os_full_name":"Debian 18.0",
         "os_kernel":"linux",
         "os_version":"18.0",
         "os_virtualization_role":"guest",
         "os_virtualization_system":"LVM",
         "hostname":"alpine-3-10-tk-01",
         "ipv4":[
            "192.168.122.111"
         ],
         "ipv6":[
            "fe80::b84f:aff:fe59:a0b1"
         ],
         "tags":[
            "Linux",
            "Datacenter 1"
         ],
         "version":"0.1.12",
         "address":"88.198.189.161:50078",
         "timezone":"UTC-0",
         "tunnels":[
            {
               "lhost":"0.0.0.0",
               "lport":"2222",
               "rhost":"0.0.0.0",
               "rport":"22",
               "lport_random":false,
               "scheme":null,
               "acl":null,
			   "idle_timeout_minutes": 0,
               "id":"1"
            },
            {
               "lhost":"0.0.0.0",
               "lport":"4000",
               "rhost":"0.0.0.0",
               "rport":"80",
               "lport_random":false,
               "scheme":null,
               "acl":null,
			   "idle_timeout_minutes": 0,
               "id":"2"
            }
         ],
         "connection_state":"connected",
         "cpu_family":"Virtual CPU",
         "cpu_model":"Virtual CPU",
         "cpu_model_name":"",
         "cpu_vendor":"GenuineIntel",
         "disconnected_at":null,
         "client_auth_id":"user1",
		 "allowed_user_groups":null,
		 "updates_status":null
      },
      {
         "id":"client-2",
         "mem_total":100000,
         "name":"Random Rport Client",
         "num_cpus":2,
         "os":"Linux alpine-3-10-tk-01 4.19.80-0-virt #1-Alpine SMP Fri Oct 18 11:51:24 UTC 2019 x86_64 Linux",
         "os_arch":"amd64",
         "os_family":"alpine",
		 "os_full_name":"Debian 18.0",
         "os_kernel":"linux",
         "os_version": "18.0",
		 "os_virtualization_role":"guest",
		 "os_virtualization_system":"LVM",
         "hostname":"alpine-3-10-tk-01",
         "ipv4":[
            "192.168.122.111"
         ],
         "ipv6":[
            "fe80::b84f:aff:fe59:a0b1"
         ],
         "tags":[
            "Linux",
            "Datacenter 1"
         ],
         "version":"0.1.12",
         "address":"88.198.189.161:50078",
         "timezone":"UTC-0",
         "tunnels":[
            {
               "lhost":"0.0.0.0",
               "lport":"2222",
               "rhost":"0.0.0.0",
               "rport":"22",
               "lport_random":false,
               "scheme":null,
               "acl":null,
			   "idle_timeout_minutes": 0,
               "id":"1"
            },
            {
               "lhost":"0.0.0.0",
               "lport":"4000",
               "rhost":"0.0.0.0",
               "rport":"80",
               "lport_random":false,
               "scheme":null,
               "acl":null,
			   "idle_timeout_minutes": 0,
               "id":"2"
            }
         ],
         "connection_state":"disconnected",
         "cpu_family":"Virtual CPU",
         "cpu_model":"Virtual CPU",
         "cpu_model_name":"",
		 "cpu_vendor":"GenuineIntel",
         "disconnected_at":"2020-08-19T13:04:23+03:00",
         "client_auth_id":"user1",
		 "allowed_user_groups":null,
		 "updates_status":null
      }
   ]
}`
	assert.Equal(t, 200, w.Code)
	assert.JSONEq(t, expectedJSON, w.Body.String())
}

func TestHandlePostMultiClientCommand(t *testing.T) {
	testUser := "test-user"
	curUser := &users.User{
		Username: testUser,
		Groups:   []string{users.Administrators},
	}

	connMock1 := test.NewConnMock()
	// by default set to return success
	connMock1.ReturnOk = true
	sshSuccessResp1 := comm.RunCmdResponse{Pid: 1, StartedAt: time.Date(2020, 10, 10, 10, 10, 1, 0, time.UTC)}
	sshRespBytes1, err := json.Marshal(sshSuccessResp1)
	require.NoError(t, err)
	connMock1.ReturnResponsePayload = sshRespBytes1

	connMock2 := test.NewConnMock()
	// by default set to return success
	connMock2.ReturnOk = true
	sshSuccessResp2 := comm.RunCmdResponse{Pid: 2, StartedAt: time.Date(2020, 10, 10, 10, 10, 2, 0, time.UTC)}
	sshRespBytes2, err := json.Marshal(sshSuccessResp2)
	require.NoError(t, err)
	connMock2.ReturnResponsePayload = sshRespBytes2

	c1 := clients.New(t).ID("client-1").Connection(connMock1).Build()
	c2 := clients.New(t).ID("client-2").Connection(connMock2).Build()
	c3 := clients.New(t).ID("client-3").DisconnectedDuration(5 * time.Minute).Build()

	defaultTimeout := 60
	gotCmd := "/bin/date;foo;whoami"
	gotCmdTimeoutSec := 30
	validReqBody := `{"command": "` + gotCmd +
		`","timeout_sec": ` + strconv.Itoa(gotCmdTimeoutSec) +
		`,"client_ids": ["` + c1.ID + `", "` + c2.ID + `"]` +
		`,"abort_on_error": false` +
		`,"execute_concurrently": false` +
		`}`

	testCases := []struct {
		name string

		requestBody string
		abortOnErr  bool

		connReturnErr error

		wantStatusCode int
		wantErrCode    string
		wantErrTitle   string
		wantErrDetail  string
		wantJobErr     string
	}{
		{
			name:           "valid cmd",
			requestBody:    validReqBody,
			wantStatusCode: http.StatusOK,
		},
		{
			name: "only one client",
			requestBody: `
		{
			"command": "/bin/date;foo;whoami",
			"timeout_sec": 30,
			"client_ids": ["client-1"]
		}`,
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   "At least 2 clients should be specified.",
		},
		{
			name: "disconnected client",
			requestBody: `
		{
			"command": "/bin/date;foo;whoami",
			"timeout_sec": 30,
			"client_ids": ["client-1", "client-3"]
		}`,
			wantStatusCode: http.StatusBadRequest,
			wantErrTitle:   fmt.Sprintf("Client with id=%q is not active.", c3.ID),
		},
		{
			name: "client not found",
			requestBody: `
		{
			"command": "/bin/date;foo;whoami",
			"timeout_sec": 30,
			"client_ids": ["client-1", "client-4"]
		}`,
			wantStatusCode: http.StatusNotFound,
			wantErrTitle:   fmt.Sprintf("Client with id=%q not found.", "client-4"),
		},
		{
			name:           "error on send request",
			requestBody:    validReqBody,
			connReturnErr:  errors.New("send fake error"),
			wantStatusCode: http.StatusOK,
			wantJobErr:     "failed to send request: send fake error",
		},
		{
			name: "error on send request, abort on err",
			requestBody: `
			{
				"command": "/bin/date;foo;whoami",
				"timeout_sec": 30,
				"client_ids": ["client-1", "client-2"],
				"execute_concurrently": false,
				"abort_on_error": true
			}`,
			abortOnErr:     true,
			connReturnErr:  errors.New("send fake error"),
			wantStatusCode: http.StatusOK,
			wantJobErr:     "failed to send request: send fake error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			al := APIListener{
				insecureForTests: true,
				Server: &Server{
					clientService: NewClientService(nil, clients.NewClientRepository([]*clients.Client{c1, c2, c3}, &hour, testLog)),
					config: &Config{
						Server: ServerConfig{
							RunRemoteCmdTimeoutSec: defaultTimeout,
							MaxRequestBytes:        1024 * 1024,
						},
					},
					jobsDoneChannel: jobResultChanMap{
						m: make(map[string]chan *models.Job),
					},
				},
				userService: users.NewAPIService(users.NewStaticProvider([]*users.User{curUser}), false),
				Logger:      testLog,
			}
			var done chan bool
			if tc.wantStatusCode == http.StatusOK {
				done = make(chan bool)
				al.testDone = done
			}

			al.initRouter()

			jp, err := jobs.NewSqliteProvider("file::memory:?cache=shared", testLog)
			require.NoError(t, err)
			defer jp.Close()
			al.jobProvider = jp

			connMock1.ReturnErr = tc.connReturnErr

			ctx := api.WithUser(context.Background(), testUser)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", strings.NewReader(tc.requestBody))
			req = req.WithContext(ctx)

			// when
			w := httptest.NewRecorder()
			al.router.ServeHTTP(w, req)

			// then
			assert.Equal(t, tc.wantStatusCode, w.Code)
			if tc.wantStatusCode == http.StatusOK {
				// wait until async task executeMultiClientJob finishes
				<-al.testDone
			}
			if tc.wantErrTitle == "" {
				// success case
				assert.Contains(t, w.Body.String(), `{"data":{"jid":`)
				gotResp := api.NewSuccessPayload(newJobResponse{})
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &gotResp))
				gotPropMap, ok := gotResp.Data.(map[string]interface{})
				require.True(t, ok)
				jidObj, found := gotPropMap["jid"]
				require.True(t, found)
				gotJID, ok := jidObj.(string)
				require.True(t, ok)
				require.NotEmpty(t, gotJID)

				gotMultiJob, err := jp.GetMultiJob(gotJID)
				require.NoError(t, err)
				require.NotNil(t, gotMultiJob)
				if tc.abortOnErr {
					require.Len(t, gotMultiJob.Jobs, 1)
				} else {
					require.Len(t, gotMultiJob.Jobs, 2)
				}
				if tc.connReturnErr != nil {
					assert.Equal(t, models.JobStatusFailed, gotMultiJob.Jobs[0].Status)
					assert.Equal(t, tc.wantJobErr, gotMultiJob.Jobs[0].Error)
				} else {
					assert.Equal(t, models.JobStatusRunning, gotMultiJob.Jobs[0].Status)
				}
				if !tc.abortOnErr {
					assert.Equal(t, models.JobStatusRunning, gotMultiJob.Jobs[1].Status)
				}
			} else {
				// failure case
				wantResp := api.NewErrAPIPayloadFromMessage(tc.wantErrCode, tc.wantErrTitle, tc.wantErrDetail)
				wantRespBytes, err := json.Marshal(wantResp)
				require.NoError(t, err)
				require.Equal(t, string(wantRespBytes), w.Body.String())
			}
		})
	}
}

func TestValidateInputClientGroup(t *testing.T) {
	testCases := []struct {
		name    string
		groupID string
		wantErr error
	}{
		{
			name:    "empty group ID",
			groupID: "",
			wantErr: errors.New("group ID cannot be empty"),
		},
		{
			name:    "group ID only with whitespaces",
			groupID: " ",
			wantErr: errors.New("group ID cannot be empty"),
		},
		{
			name:    "group ID with invalid char '?'",
			groupID: "?",
			wantErr: errors.New(`invalid group ID "?": can contain only "A-Za-z0-9_-*"`),
		},
		{
			name:    "group ID with invalid char '.'",
			groupID: "2.1",
			wantErr: errors.New(`invalid group ID "2.1": can contain only "A-Za-z0-9_-*"`),
		},
		{
			name:    "group ID with extra whitespaces",
			groupID: " id ",
			wantErr: errors.New(`invalid group ID " id ": can contain only "A-Za-z0-9_-*"`),
		},
		{
			name:    "group ID with invalid char '/'",
			groupID: "2/1",
			wantErr: errors.New(`invalid group ID "2/1": can contain only "A-Za-z0-9_-*"`),
		},
		{
			name:    "valid group ID with all available chars",
			groupID: "*abc-XYZ_09_ABC-xyz*",
			wantErr: nil,
		},
		{
			name:    "valid group ID with one char",
			groupID: "a",
			wantErr: nil,
		},
		{
			name:    "valid group ID with one char '*'",
			groupID: "*",
			wantErr: nil,
		},
		{
			name:    "valid group ID with max number of chars",
			groupID: "012345678901234567890123456789",
			wantErr: nil,
		},
		{
			name:    "invalid group ID with too many chars",
			groupID: "0123456789012345678901234567890",
			wantErr: errors.New("invalid group ID: max length 30, got 31"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// when
			gotErr := validateInputClientGroup(cgroups.ClientGroup{ID: tc.groupID})

			// then
			assert.Equal(t, tc.wantErr, gotErr)
		})
	}
}

func TestHandleRefreshUpdatesStatus(t *testing.T) {
	c1 := clients.New(t).Build()
	c2 := clients.New(t).DisconnectedDuration(5 * time.Minute).Build()

	testCases := []struct {
		Name                string
		ClientID            string
		SSHError            bool
		ExpectedStatus      int
		ExpectedRequestName string
	}{
		{
			Name:                "Connected client",
			ClientID:            c1.ID,
			ExpectedStatus:      http.StatusNoContent,
			ExpectedRequestName: comm.RequestTypeRefreshUpdatesStatus,
		},
		{
			Name:           "Disconnected client",
			ClientID:       c2.ID,
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Name:           "Non-existing client",
			ClientID:       "non-existing-client",
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Name:                "SSH error",
			ClientID:            c1.ID,
			SSHError:            true,
			ExpectedRequestName: comm.RequestTypeRefreshUpdatesStatus,
			ExpectedStatus:      http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			connMock := test.NewConnMock()
			// by default set to return success
			connMock.ReturnOk = !tc.SSHError
			c1.Connection = connMock

			al := APIListener{
				insecureForTests: true,
				Server: &Server{
					clientService: NewClientService(nil, clients.NewClientRepository([]*clients.Client{c1, c2}, &hour, testLog)),
					config:        &Config{},
				},
				Logger: testLog,
			}
			al.initRouter()

			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/clients/%s/updates-status", tc.ClientID), nil)

			w := httptest.NewRecorder()
			al.router.ServeHTTP(w, req)

			assert.Equal(t, tc.ExpectedStatus, w.Code)
			if tc.ExpectedRequestName != "" {
				name, _, _ := connMock.InputSendRequest()
				assert.Equal(t, tc.ExpectedRequestName, name)
			}
		})
	}
}

func TestHandleGetClient(t *testing.T) {
	c1 := clients.New(t).ID("client-1").ClientAuthID(cl1.ID).Build()
	al := APIListener{
		insecureForTests: true,
		Server: &Server{
			clientService: NewClientService(nil, clients.NewClientRepository([]*clients.Client{c1}, &hour, testLog)),
			config: &Config{
				Server: ServerConfig{MaxRequestBytes: 1024 * 1024},
			},
		},
	}
	al.initRouter()

	testCases := []struct {
		Name           string
		URL            string
		ExpectedStatus int
	}{
		{
			Name:           "Ok",
			URL:            "/api/v1/clients/client-1",
			ExpectedStatus: http.StatusOK,
		}, {
			Name:           "Not found",
			URL:            "/api/v1/clients/client-2",
			ExpectedStatus: http.StatusNotFound,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", tc.URL, nil)
			al.router.ServeHTTP(w, req)

			expectedJSON := `{
    "data":{
        "id":"client-1",
        "mem_total":100000,
        "name":"Random Rport Client",
        "num_cpus":2,
        "os":"Linux alpine-3-10-tk-01 4.19.80-0-virt #1-Alpine SMP Fri Oct 18 11:51:24 UTC 2019 x86_64 Linux",
        "os_arch":"amd64",
        "os_family":"alpine",
        "os_full_name":"Debian 18.0",
        "os_kernel":"linux",
        "os_version":"18.0",
        "os_virtualization_role":"guest",
        "os_virtualization_system":"LVM",
        "hostname":"alpine-3-10-tk-01",
        "ipv4":[
            "192.168.122.111"
        ],
        "ipv6":[
            "fe80::b84f:aff:fe59:a0b1"
        ],
        "tags":[
            "Linux",
            "Datacenter 1"
        ],
        "version":"0.1.12",
        "address":"88.198.189.161:50078",
        "timezone":"UTC-0",
        "tunnels":[
            {
                "lhost":"0.0.0.0",
                "lport":"2222",
                "rhost":"0.0.0.0",
                "rport":"22",
                "lport_random":false,
                "scheme":null,
                "acl":null,
		        "idle_timeout_minutes": 0,
                "id":"1"
            },
            {
                "lhost":"0.0.0.0",
                "lport":"4000",
                "rhost":"0.0.0.0",
                "rport":"80",
                "lport_random":false,
                "scheme":null,
                "acl":null,
		        "idle_timeout_minutes": 0,
                "id":"2"
            }
        ],
        "connection_state":"connected",
        "cpu_family":"Virtual CPU",
        "cpu_model":"Virtual CPU",
        "cpu_model_name":"",
        "cpu_vendor":"GenuineIntel",
        "disconnected_at":null,
        "client_auth_id":"user1",
        "allowed_user_groups":null,
        "updates_status":null
    }
}`
			assert.Equal(t, tc.ExpectedStatus, w.Code)
			if tc.ExpectedStatus == http.StatusOK {
				assert.JSONEq(t, expectedJSON, w.Body.String())
			}
		})
	}
}

type MockUsersService struct {
	UserService

	ChangeUser     *users.User
	ChangeUsername string
}

func (s *MockUsersService) Change(user *users.User, username string) error {
	s.ChangeUser = user
	s.ChangeUsername = username
	return nil
}

func TestPostToken(t *testing.T) {
	user := &users.User{
		Username: "test-user",
	}
	mockUsersService := &MockUsersService{
		UserService: users.NewAPIService(users.NewStaticProvider([]*users.User{user}), false),
	}

	uuid := "cb5b6578-94f5-4a5b-af58-f7867a943b0c"
	oldUUID := random.UUID4
	random.UUID4 = func() (string, error) {
		return uuid, nil
	}
	defer func() {
		random.UUID4 = oldUUID
	}()

	al := APIListener{
		insecureForTests: true,
		Server: &Server{
			config: &Config{},
		},
		userService: mockUsersService,
	}
	al.initRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/me/token", nil)
	ctx := api.WithUser(req.Context(), user.Username)
	req = req.WithContext(ctx)
	al.router.ServeHTTP(w, req)

	expectedJSON := `{"data":{"token":"` + uuid + `"}}`
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, expectedJSON, w.Body.String())

	expectedUser := &users.User{
		Token: &uuid,
	}
	assert.Equal(t, user.Username, mockUsersService.ChangeUsername)
	assert.Equal(t, expectedUser, mockUsersService.ChangeUser)
}

func TestDeleteToken(t *testing.T) {
	user := &users.User{
		Username: "test-user",
	}
	mockUsersService := &MockUsersService{
		UserService: users.NewAPIService(users.NewStaticProvider([]*users.User{user}), false),
	}
	noToken := ""
	al := APIListener{
		insecureForTests: true,
		Server: &Server{
			config: &Config{},
		},
		userService: mockUsersService,
	}
	al.initRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/me/token", nil)
	ctx := api.WithUser(req.Context(), user.Username)
	req = req.WithContext(ctx)
	al.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	expectedUser := &users.User{
		Token: &noToken,
	}
	assert.Equal(t, user.Username, mockUsersService.ChangeUsername)
	assert.Equal(t, expectedUser, mockUsersService.ChangeUser)
}

func TestWrapWithAuthMiddleware(t *testing.T) {
	user := &users.User{
		Username: "user1",
		Password: "$2y$05$ep2DdPDeLDDhwRrED9q/vuVEzRpZtB5WHCFT7YbcmH9r9oNmlsZOm",
		Token:    ptr.String("$2y$05$/D7g/d0sDkNSOh.e6Jzc9OWClcpZ1ieE8Dx.WUaWgayd3Ab0rRdxu"),
	}
	userWithoutToken := &users.User{
		Username: "user2",
		Password: "$2y$05$ep2DdPDeLDDhwRrED9q/vuVEzRpZtB5WHCFT7YbcmH9r9oNmlsZOm",
		Token:    nil,
	}
	al := APIListener{
		apiSessionRepo: NewAPISessionRepository(),
		bannedUsers:    security.NewBanList(0),
		userService:    users.NewAPIService(users.NewStaticProvider([]*users.User{user, userWithoutToken}), false),
		Server: &Server{
			config: &Config{},
		},
	}
	jwt, err := al.createAuthToken(time.Hour, user.Username)
	require.NoError(t, err)

	testCases := []struct {
		Name           string
		Username       string
		Password       string
		EnableTwoFA    bool
		Bearer         string
		ExpectedStatus int
	}{
		{
			Name:           "no auth",
			ExpectedStatus: http.StatusUnauthorized,
		},
		{
			Name:           "basic auth with password",
			Username:       user.Username,
			Password:       "pwd",
			ExpectedStatus: http.StatusOK,
		},
		{
			Name:           "basic auth with password, no password",
			Username:       user.Username,
			Password:       "",
			ExpectedStatus: http.StatusUnauthorized,
		},
		{
			Name:           "basic auth with password, wrong password",
			Username:       user.Username,
			Password:       "wrong",
			ExpectedStatus: http.StatusUnauthorized,
		},
		{
			Name:           "basic auth with password, 2fa enabled",
			Username:       user.Username,
			Password:       "pwd",
			EnableTwoFA:    true,
			ExpectedStatus: http.StatusUnauthorized,
		},
		{
			Name:           "basic auth with token",
			Username:       user.Username,
			Password:       "token",
			ExpectedStatus: http.StatusOK,
		},
		{
			Name:           "basic auth with token, 2fa enabled",
			Username:       user.Username,
			Password:       "token",
			EnableTwoFA:    true,
			ExpectedStatus: http.StatusOK,
		},
		{
			Name:           "basic auth with token, wrong token",
			Username:       user.Username,
			Password:       "wrong-token",
			ExpectedStatus: http.StatusUnauthorized,
		},
		{
			Name:           "basic auth with token, user has no token",
			Username:       userWithoutToken.Username,
			Password:       "",
			ExpectedStatus: http.StatusUnauthorized,
		},
		{
			Name:           "bearer token",
			ExpectedStatus: http.StatusOK,
			Bearer:         jwt,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			twoFATokenDelivery := ""
			if tc.EnableTwoFA {
				twoFATokenDelivery = "smtp"
			}
			al.config.API.TwoFATokenDelivery = twoFATokenDelivery

			handler := al.wrapWithAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, user.Username, api.GetUser(r.Context(), nil))
			}))

			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/some-endpoint", nil)
			if tc.Username != "" {
				req.SetBasicAuth(tc.Username, tc.Password)
			}
			if tc.Bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.Bearer)
			}

			handler(w, req)

			assert.Equal(t, tc.ExpectedStatus, w.Code)
		})
	}
}
