package nss_test

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/ubuntu/authd/internal/testutils"
	"github.com/ubuntu/authd/internal/testutils/golden"
	localgroupstestutils "github.com/ubuntu/authd/internal/users/localentries/testutils"
)

var daemonPath string

func TestIntegration(t *testing.T) {
	t.Parallel()

	// codeNotFound is the expected exit code for the getent subprocess in case of errors.
	const codeNotFound int = 2

	libPath, rustCovEnv := buildRustNSSLib(t)

	// Create a default daemon to use for most test cases.
	defaultSocket := filepath.Join(os.TempDir(), "nss-integration-tests.sock")
	defaultDbState := "multiple_users_and_groups"
	defaultOutputPath := filepath.Join(filepath.Dir(daemonPath), "gpasswd.output")
	defaultGroupsFilePath := filepath.Join(testutils.TestFamilyPath(t), "gpasswd.group")

	env := append(localgroupstestutils.AuthdIntegrationTestsEnvWithGpasswdMock(t, defaultOutputPath, defaultGroupsFilePath), "AUTHD_INTEGRATIONTESTS_CURRENT_USER_AS_ROOT=1")
	ctx, cancel := context.WithCancel(context.Background())
	_, stopped := testutils.RunDaemon(ctx, t, daemonPath,
		testutils.WithSocketPath(defaultSocket),
		testutils.WithPreviousDBState(defaultDbState),
		testutils.WithEnvironment(env...),
	)

	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	tests := map[string]struct {
		db      string
		key     string
		cacheDB string

		noDaemon           bool
		currentUserNotRoot bool
		wantSecondCall     bool
		shouldPreCheck     bool

		wantStatus int
	}{
		"Get_all_entries_from_passwd":                    {db: "passwd"},
		"Get_all_entries_from_group":                     {db: "group"},
		"Get_all_entries_from_shadow_if_considered_root": {db: "shadow"},

		"Get_entry_from_passwd_by_name":                    {db: "passwd", key: "user1"},
		"Get_entry_from_group_by_name":                     {db: "group", key: "group1"},
		"Get_entry_from_shadow_by_name_if_considered_root": {db: "shadow", key: "user1"},

		"Get_entry_from_passwd_by_id": {db: "passwd", key: "1111"},
		"Get_entry_from_group_by_id":  {db: "group", key: "11111"},

		"Check_user_with_broker_if_not_found_in_cache": {db: "passwd", key: "user-pre-check", shouldPreCheck: true},

		// Even though those are "error" cases, the getent command won't fail when trying to list content of a service.
		"Returns_empty_when_getting_all_entries_from_shadow_if_regular_user": {db: "shadow", currentUserNotRoot: true},

		"Returns_empty_when_getting_all_entries_from_passwd_and_daemon_is_not_available": {db: "passwd", noDaemon: true},
		"Returns_empty_when_getting_all_entries_from_group_and_daemon_is_not_available":  {db: "group", noDaemon: true},
		"Returns_empty_when_getting_all_entries_from_shadow_and_daemon_is_not_available": {db: "shadow", noDaemon: true},

		/* Error cases */
		// We can't assert on the returned error type since the error returned by getent will always be 2 (i.e. Not Found), even though the library returns other types.
		"Error_when_getting_all_entries_from_passwd_and_database_is_corrupted": {db: "passwd", cacheDB: "invalid_entry_in_userByID", wantSecondCall: true},
		"Error_when_getting_all_entries_from_group_and_database_is_corrupted":  {db: "group", cacheDB: "invalid_entry_in_groupByID", wantSecondCall: true},
		"Error_when_getting_all_entries_from_shadow_and_database_is_corrupted": {db: "shadow", cacheDB: "invalid_entry_in_userByID", wantSecondCall: true},

		"Error_when_getting_shadow_by_name_if_regular_user": {db: "shadow", key: "user1", currentUserNotRoot: true, wantStatus: codeNotFound},

		"Error_when_getting_passwd_by_name_and_entry_does_not_exist":                        {db: "passwd", key: "doesnotexit", wantStatus: codeNotFound},
		"Error_when_getting_passwd_by_name_entry_exists_in_broker_but_precheck_is_disabled": {db: "passwd", key: "user-pre-check", wantStatus: codeNotFound},
		"Error_when_getting_group_by_name_and_entry_does_not_exist":                         {db: "group", key: "doesnotexit", wantStatus: codeNotFound},
		"Error_when_getting_shadow_by_name_and_entry_does_not_exist":                        {db: "shadow", key: "doesnotexit", wantStatus: codeNotFound},

		"Error_when_getting_passwd_by_id_and_entry_does_not_exist": {db: "passwd", key: "404", wantStatus: codeNotFound},
		"Error_when_getting_group_by_id_and_entry_does_not_exist":  {db: "group", key: "404", wantStatus: codeNotFound},

		"Error_when_getting_passwd_by_name_and_daemon_is_not_available": {db: "passwd", key: "user1", noDaemon: true, wantStatus: codeNotFound},
		"Error_when_getting_group_by_name_and_daemon_is_not_available":  {db: "group", key: "group1", noDaemon: true, wantStatus: codeNotFound},
		"Error_when_getting_shadow_by_name_and_daemon_is_not_available": {db: "shadow", key: "user1", noDaemon: true, wantStatus: codeNotFound},

		"Error_when_getting_passwd_by_id_and_daemon_is_not_available": {db: "passwd", key: "1111", noDaemon: true, wantStatus: codeNotFound},
		"Error_when_getting_group_by_id_and_daemon_is_not_available":  {db: "group", key: "11111", noDaemon: true, wantStatus: codeNotFound},

		/* Special cases */
		"Do_not_query_the_cache_when_user_is_pam_unix_non_existent": {db: "passwd", key: "pam_unix_non_existent:", cacheDB: "pam_unix_non_existent", wantStatus: codeNotFound},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			socketPath := defaultSocket

			var useAlternativeDaemon bool
			if tc.cacheDB != "" || tc.currentUserNotRoot {
				useAlternativeDaemon = true
			} else {
				tc.cacheDB = defaultDbState
			}

			// We don't check compatibility of arguments, have noDaemon taking precedences to the others.
			if tc.noDaemon {
				socketPath = ""
				useAlternativeDaemon = false
			}

			if useAlternativeDaemon {
				// Run a specific new daemon for special test cases.
				outPath := filepath.Join(t.TempDir(), "gpasswd.output")
				groupsFilePath := filepath.Join("testdata", "empty.group")

				var daemonStopped chan struct{}
				ctx, cancel := context.WithCancel(context.Background())
				env := localgroupstestutils.AuthdIntegrationTestsEnvWithGpasswdMock(t, outPath, groupsFilePath)
				if !tc.currentUserNotRoot {
					env = append(env, "AUTHD_INTEGRATIONTESTS_CURRENT_USER_AS_ROOT=1")
				}
				socketPath, daemonStopped = testutils.RunDaemon(ctx, t, daemonPath,
					testutils.WithPreviousDBState(tc.cacheDB),
					testutils.WithEnvironment(env...),
				)
				t.Cleanup(func() {
					cancel()
					<-daemonStopped
				})
			}

			cmds := []string{tc.db}
			if tc.key != "" {
				cmds = append(cmds, tc.key)
			}

			got, status := getentOutputForLib(t, libPath, socketPath, rustCovEnv, tc.shouldPreCheck, cmds...)
			require.Equal(t, tc.wantStatus, status, "Expected status %d, but got %d", tc.wantStatus, status)

			if tc.shouldPreCheck && tc.db == "passwd" {
				// When pre-checking, the `getent passwd` output contains a randomly generated UID.
				// To make the test deterministic, we replace the UID with a placeholder.
				// The output looks something like this:
				//     user-pre-check:x:1776689191:0:gecos for user-pre-check:/home/user-pre-check:/usr/bin/bash\n
				fields := strings.Split(got, ":")
				require.Len(t, fields, 7, "Invalid number of fields in the output: %q", got)
				// The UID is the third field.
				fields[2] = "{{UID}}"
				got = strings.Join(fields, ":")
			}

			// If the exit status is NotFound, there is no need to create an empty golden file.
			// But we need to ensure that the output is indeed empty.
			if tc.wantStatus == codeNotFound {
				require.Empty(t, got, "Expected empty output, but got %q", got)
				return
			}

			golden.CheckOrUpdate(t, got)

			// This is to check that some cache tasks, such as cleaning a corrupted database, work as expected.
			if tc.wantSecondCall {
				got, status := getentOutputForLib(t, libPath, socketPath, rustCovEnv, tc.shouldPreCheck, cmds...)
				require.NotEqual(t, codeNotFound, status, "Expected no error, but got %v", status)
				require.Empty(t, got, "Expected empty output, but got %q", got)
			}
		})
	}
}

func TestMockgpasswd(t *testing.T) {
	localgroupstestutils.Mockgpasswd(t)
}

func TestMain(m *testing.M) {
	// Needed to skip the test setup when running the gpasswd mock.
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "" {
		os.Exit(m.Run())
	}

	execPath, cleanup, err := testutils.BuildDaemon("-tags=withexamplebroker,integrationtests")
	if err != nil {
		log.Printf("Setup: failed to build daemon: %v", err)
		os.Exit(1)
	}
	defer cleanup()
	daemonPath = execPath

	m.Run()
}
