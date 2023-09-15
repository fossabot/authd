package brokers_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/ubuntu/authd/internal/brokers"
	"github.com/ubuntu/authd/internal/testutils"
)

var (
	brokerCfgs = filepath.Join("testdata", "broker.d")
)

func TestNewManager(t *testing.T) {
	tests := map[string]struct {
		cfgDir string
		noBus  bool

		wantErr bool
	}{
		"Creates all brokers when config dir has only valid brokers":                 {cfgDir: "valid_brokers"},
		"Creates only correct brokers when config dir has valid and invalid brokers": {cfgDir: "mixed_brokers"},
		"Creates only local broker when config dir has only invalid ones":            {cfgDir: "invalid_brokers"},
		"Creates only local broker when config dir does not exist":                   {cfgDir: "does/not/exist"},
		"Creates manager even if broker is not exported on dbus":                     {cfgDir: "not_on_bus"},

		"Error when can't connect to system bus": {cfgDir: "valid_brokers", noBus: true, wantErr: true},
		"Error when broker config dir is a file": {cfgDir: "file_config_dir", wantErr: true},
	}
	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			if tc.noBus {
				t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "/dev/null")
			}

			got, err := brokers.NewManager(context.Background(), nil, brokers.WithRootDir(brokerCfgs), brokers.WithCfgDir(tc.cfgDir))
			if tc.wantErr {
				require.Error(t, err, "NewManager should return an error, but did not")
				return
			}
			require.NoError(t, err, "NewManager should not return an error, but did")

			// Grab the list of broker names from the manager to use as golden file.
			var brokers []string
			for _, broker := range got.AvailableBrokers() {
				brokers = append(brokers, broker.Name)
			}

			want := testutils.LoadWithUpdateFromGoldenYAML(t, brokers)
			require.Equal(t, want, brokers, "NewManager should return the expected brokers, but did not")
		})
	}
}

func TestSetDefaultBrokerForUser(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		exists bool

		wantErr bool
	}{
		"Successfully assigns existent broker to user": {exists: true},

		"Error when broker does not exist": {wantErr: true},
	}
	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, err := brokers.NewManager(context.Background(), nil, brokers.WithRootDir(brokerCfgs), brokers.WithCfgDir("mixed_brokers"))
			require.NoError(t, err, "Setup: could not create manager")

			want := m.AvailableBrokers()[0]
			if !tc.exists {
				want.ID = "does not exist"
			}

			err = m.SetDefaultBrokerForUser(want.ID, "user")
			if tc.wantErr {
				require.Error(t, err, "SetDefaultBrokerForUser should return an error, but did not")
				return
			}
			require.NoError(t, err, "SetDefaultBrokerForUser should not return an error, but did")

			got := m.BrokerForUser("user")
			require.Equal(t, want.ID, got.ID, "SetDefaultBrokerForUser should have assiged the expected broker, but did not")
		})
	}
}

func TestBrokerForUser(t *testing.T) {
	t.Parallel()

	m, err := brokers.NewManager(context.Background(), nil, brokers.WithRootDir(brokerCfgs), brokers.WithCfgDir("valid_brokers"))
	require.NoError(t, err, "Setup: could not create manager")

	err = m.SetDefaultBrokerForUser("local", "user")
	require.NoError(t, err, "Setup: could not set default broker")

	// Broker for user should return the assigned broker
	got := m.BrokerForUser("user")
	require.Equal(t, "local", got.ID, "BrokerForUser should return the assigned broker, but did not")

	// Broker for user should return nil if no broker is assigned
	got = m.BrokerForUser("no_broker")
	require.Nil(t, got, "BrokerForUser should return nil if no broker is assigned, but did not")
}

func TestBrokerFromSessionID(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		sessionID string

		wantBrokerID string
		wantErr      bool
	}{
		"Successfully returns expected broker":       {sessionID: "success"},
		"Returns local broker if sessionID is empty": {wantBrokerID: "local"},

		"Error if broker does not exist": {sessionID: "does not exist", wantErr: true},
	}
	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfgDir := t.TempDir()
			b := newBrokerForTests(t, cfgDir, "")
			m, err := brokers.NewManager(context.Background(), nil, brokers.WithCfgDir(cfgDir))
			require.NoError(t, err, "Setup: could not create manager")

			if tc.sessionID == "success" {
				// We need to use the ID generated by the mananger.
				for _, broker := range m.AvailableBrokers() {
					if broker.Name != b.Name {
						continue
					}
					b.ID = broker.ID
					break
				}
				tc.wantBrokerID = b.ID
				m.SetBrokerForSession(&b, tc.sessionID)
			}

			got, err := m.BrokerFromSessionID(tc.sessionID)
			if tc.wantErr {
				require.Error(t, err, "BrokerFromSessionID should return an error, but did not")
				return
			}
			require.NoError(t, err, "BrokerFromSessionID should not return an error, but did")
			require.Equal(t, tc.wantBrokerID, got.ID, "BrokerFromSessionID should return the expected broker, but did not")
		})
	}
}

func TestNewSession(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		brokerID string
		username string

		wantErr bool
	}{
		"Successfully start a new session": {username: "success"},

		"Error when broker does not exist":         {brokerID: "does_not_exist", wantErr: true},
		"Error when broker does not provide an ID": {username: "NS_no_id", wantErr: true},
		"Error when starting a new session":        {username: "NS_error", wantErr: true},
	}
	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfgDir := t.TempDir()
			wantBroker := newBrokerForTests(t, cfgDir)
			m, err := brokers.NewManager(context.Background(), nil, brokers.WithCfgDir(cfgDir))
			require.NoError(t, err, "Setup: could not create manager")

			if tc.brokerID == "" {
				// We need to use the ID generated by the mananger.
				for _, broker := range m.AvailableBrokers() {
					if broker.Name != wantBroker.Name {
						continue
					}
					wantBroker.ID = broker.ID
				}
				tc.brokerID = wantBroker.ID
			}

			gotID, gotEKey, err := m.NewSession(tc.brokerID, tc.username, "some_lang")
			if tc.wantErr {
				require.Error(t, err, "NewSession should return an error, but did not")
				return
			}
			require.NoError(t, err, "NewSession should not return an error, but did")

			// Replaces the autogenerated part of the ID with a placeholder before saving the file.
			gotStr := fmt.Sprintf("ID: %s\nEncryption Key: %s\n", strings.ReplaceAll(gotID, wantBroker.ID, "BROKER_ID"), gotEKey)
			wantStr := testutils.LoadWithUpdateFromGolden(t, gotStr)
			require.Equal(t, wantStr, gotStr, "NewSession should return the expected session, but did not")

			gotBroker, err := m.BrokerFromSessionID(gotID)
			require.NoError(t, err, "NewSession should have assigned a broker for the session, but did not")
			require.Equal(t, wantBroker.ID, gotBroker.ID, "BrokerFromSessionID should have assigned the expected broker for the session, but did not")
		})
	}
}

func TestEndSession(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		brokerID  string
		sessionID string

		wantErr bool
	}{
		"Successfully end session": {sessionID: "success"},

		"Error when broker does not exist": {brokerID: "does not exist", sessionID: "dont matter", wantErr: true},
		"Error when ending session":        {sessionID: "ES_error", wantErr: true},
	}
	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfgDir := t.TempDir()
			b := newBrokerForTests(t, cfgDir)
			m, err := brokers.NewManager(context.Background(), nil, brokers.WithCfgDir(cfgDir))
			require.NoError(t, err, "Setup: could not create manager")

			if tc.brokerID != "does not exist" {
				m.SetBrokerForSession(&b, tc.sessionID)
			}

			err = m.EndSession(tc.sessionID)
			if tc.wantErr {
				require.Error(t, err, "EndSession should return an error, but did not")
				return
			}
			require.NoError(t, err, "EndSession should not return an error, but did")
			_, err = m.BrokerFromSessionID(tc.sessionID)
			require.Error(t, err, "EndSession should have removed the broker from the active transactions, but did not")
		})
	}
}

func TestMain(m *testing.M) {
	testutils.InstallUpdateFlag()
	flag.Parse()

	// Start system bus mock.
	cleanup, err := testutils.StartSystemBusMock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	m.Run()
}
