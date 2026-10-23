package main

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/spf13/cobra"
)

var knowledgeCmd = &cobra.Command{Use: "knowledge", Short: "Manage local text knowledge references"}
var knowledgeRegisterCmd = &cobra.Command{
	Use: "register <file|folder|skill> <path>", Short: "Approve a local text reference for task context without uploading it", Args: exactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inDaemonManagedExecutionContext() {
			return errors.New("knowledge registration is unavailable inside a task")
		}
		allow, _ := cmd.Flags().GetBool("allow-read")
		approve, _ := cmd.Flags().GetBool("approve-context")
		if !allow || !approve {
			return errors.New("--allow-read and --approve-context are required")
		}
		profile, _ := cmd.Flags().GetString("profile")
		cfg, err := cli.LoadCLIConfigForProfile(profile)
		if err != nil {
			return err
		}
		server, _ := cmd.Flags().GetString("server-url")
		if server == "" {
			server = cfg.ServerURL
		}
		server, err = daemon.NormalizeServerBaseURL(server)
		if err != nil {
			return err
		}
		workspaceID, _ := cmd.Flags().GetString("workspace-id")
		if workspaceID == "" {
			workspaceID = cfg.WorkspaceID
		}
		ownerID, _ := cmd.Flags().GetString("owner-id")
		dir, err := cli.ProfileDir(profile)
		if err != nil {
			return err
		}
		daemonID, err := daemon.EnsureDaemonID(profile)
		if err != nil {
			return err
		}
		kind := "knowledge_" + args[0]
		ref, err := (knowledge.LocalStore{Dir: filepath.Join(dir, "knowledge-v1")}).Register(cmd.Context(), knowledge.Scope{Server: server, WorkspaceID: workspaceID, OwnerID: ownerID, DaemonID: daemonID}, kind, args[1], allow, approve)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"resource_type": kind, "resource_ref": ref})
	},
}

func init() {
	knowledgeCmd.GroupID = groupRuntime
	rootCmd.AddCommand(knowledgeCmd)
	knowledgeCmd.AddCommand(knowledgeRegisterCmd)
	knowledgeRegisterCmd.Flags().String("owner-id", "", "Owning runtime user's stable ID (required)")
	knowledgeRegisterCmd.Flags().Bool("allow-read", false, "Allow bounded inspection on this machine")
	knowledgeRegisterCmd.Flags().Bool("approve-context", false, "Approve the inspected digest for text context only")
}
