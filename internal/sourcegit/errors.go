package sourcegit

type RemoteFailureCode string

const (
	RemoteInvalidURL      RemoteFailureCode = "invalid_url"
	RemotePolicyDenied    RemoteFailureCode = "policy_denied"
	RemoteDNS             RemoteFailureCode = "dns"
	RemoteTimeout         RemoteFailureCode = "timeout"
	RemoteConnection      RemoteFailureCode = "connection"
	RemoteAuthentication  RemoteFailureCode = "authentication"
	RemoteRefNotFound     RemoteFailureCode = "ref_not_found"
	RemoteOutputTooLarge  RemoteFailureCode = "output_too_large"
	RemoteInvalidResponse RemoteFailureCode = "invalid_response"
)

var remoteSafeMessages = map[RemoteFailureCode]string{
	RemoteInvalidURL:      "Repository URL is invalid.",
	RemotePolicyDenied:    "Repository network policy denied the connection.",
	RemoteDNS:             "Repository host could not be resolved safely.",
	RemoteTimeout:         "Repository validation timed out.",
	RemoteConnection:      "Repository connection failed.",
	RemoteAuthentication:  "Repository authentication failed.",
	RemoteRefNotFound:     "Repository ref was not found.",
	RemoteOutputTooLarge:  "Repository response exceeded the size limit.",
	RemoteInvalidResponse: "Repository returned an invalid response.",
}

type RemoteError struct {
	Code      RemoteFailureCode
	Retryable bool
}

func (err *RemoteError) Error() string {
	if message, ok := remoteSafeMessages[err.Code]; ok {
		return message
	}
	return "Repository validation failed."
}

type RepositoryFailureCode string

const (
	RepositoryInvalidRef     RepositoryFailureCode = "invalid_ref"
	RepositoryGit            RepositoryFailureCode = "git"
	RepositoryTimeout        RepositoryFailureCode = "timeout"
	RepositoryOutputTooLarge RepositoryFailureCode = "output_too_large"
	RepositoryUnsafeTree     RepositoryFailureCode = "unsafe_tree"
	RepositorySubmodule      RepositoryFailureCode = "submodule"
	RepositorySymlink        RepositoryFailureCode = "symlink"
	RepositoryGitLFS         RepositoryFailureCode = "git_lfs"
	RepositorySnapshotLimit  RepositoryFailureCode = "snapshot_limit"
	RepositoryInvalidMirror  RepositoryFailureCode = "invalid_mirror"
)

var repositorySafeMessages = map[RepositoryFailureCode]string{
	RepositoryInvalidRef:     "Repository ref is invalid.",
	RepositoryGit:            "Repository Git operation failed.",
	RepositoryTimeout:        "Repository Git operation timed out.",
	RepositoryOutputTooLarge: "Repository Git output exceeded its limit.",
	RepositoryUnsafeTree:     "Repository tree contains an unsafe path or entry.",
	RepositorySubmodule:      "Repository submodules are not supported.",
	RepositorySymlink:        "Repository symlinks are not supported.",
	RepositoryGitLFS:         "Repository Git LFS pointer files are not supported.",
	RepositorySnapshotLimit:  "Repository snapshot exceeds its configured limits.",
	RepositoryInvalidMirror:  "Repository mirror is invalid.",
}

type RepositoryError struct {
	Code RepositoryFailureCode
}

func (err *RepositoryError) Error() string {
	if message, ok := repositorySafeMessages[err.Code]; ok {
		return message
	}
	return "Repository acquisition failed."
}

func remoteError(code RemoteFailureCode, retryable ...bool) *RemoteError {
	err := &RemoteError{Code: code}
	if len(retryable) != 0 {
		err.Retryable = retryable[0]
	}
	return err
}

func repositoryError(code RepositoryFailureCode) *RepositoryError {
	return &RepositoryError{Code: code}
}
