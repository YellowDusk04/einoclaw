package main

type (
	config struct {
		Models []struct {
			APIKey         string        `yaml:"api_key"`
			BaseURL        string        `yaml:"base_url"`
			Provider       ModelProvider `yaml:"provider"`
			ModelName      string        `yaml:"model_name"`
			ModelID        string        `yaml:"model_id"`
			EnableThinking bool          `yaml:"enable_thinking"`
		} `yaml:"models"`

		CozeLoop struct {
			Enabled     bool   `yaml:"enabled"`
			APIToken    string `yaml:"api_token"`
			WorkspaceID string `yaml:"workspace_id"`
		} `yaml:"coze_loop"`

		Handlers struct {
			Filesystem struct {
				Enabled   bool `yaml:"enabled"`
				Ls        bool `yaml:"ls"`
				ReadFile  bool `yaml:"read_file"`
				WriteFile bool `yaml:"write_file"`
				EditFile  bool `yaml:"edit_file"`
				Glob      bool `yaml:"glob"`
				Grep      bool `yaml:"grep"`
				Execute   bool `yaml:"execute"`
			} `yaml:"filesystem"`

			PatchToolCalls struct {
				Enabled bool `yaml:"enabled"`
			} `yaml:"patch_tool_calls"`

			Summarization struct {
				Enabled       bool `yaml:"enabled"`
				ContextTokens int  `yaml:"context_tokens"`
			} `yaml:"summarization"`

			Reduction struct {
				Enabled                   bool  `yaml:"enabled"`
				MaxLengthForTrunc         int   `yaml:"max_length_for_trunc"`
				MaxTokensForClear         int64 `yaml:"max_tokens_for_clear"`
				ClearAtLeastTokens        int64 `yaml:"clear_at_least_tokens"`
				ClearRetentionSuffixLimit int   `yaml:"clear_retention_suffix_limit"`
			} `yaml:"reduction"`

			AutoMemory struct {
				Enabled bool `yaml:"enabled"`
			} `yaml:"automemory"`
			Permission struct {
				Enabled   bool     `yaml:"enabled"`
				BlackList []string `yaml:"black_list"`
			} `yaml:"permission"`

			Skill struct {
				Enabled bool   `yaml:"enabled"`
				Dir     string `yaml:"dir"`
			} `yaml:"skill"`
		} `yaml:"handlers"`
	}
)
