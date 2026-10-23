package domain

// The user-visible texts of the GitHub failures of an export (spec 7.6.7).
const (
	PermissionDeniedMessage = "GitHub refused the write. The studio token needs the classic scopes `repo` + `workflow`, " +
		"or the fine-grained permissions Contents:write, Workflows:write and Pull requests:write."
	UnavailableMessage = "GitHub is unavailable, try again later"
)

// GitHubErrorCode maps a failed GitHub call of a refresh to the error code of the installation
// (spec 7.7.1). status is the HTTP status, 0 for a failure without a response; rateLimited is true
// when the response said x-ratelimit-remaining: 0. The code never carries message text.
func GitHubErrorCode(status int, rateLimited bool) ErrorCode {
	switch status {
	case 429:
		return CodeRateLimited
	case 401, 403:
		if rateLimited {
			return CodeRateLimited
		}
		return CodePermissionDenied
	}
	return CodeUnavailable
}

// PublishErrorCode maps a failed GitHub write of an export to the code it answers with (spec
// 7.6.7): 403 and 404 are a permission problem of the studio token, anything else is
// unavailability.
func PublishErrorCode(status int) ErrorCode {
	if status == 403 || status == 404 {
		return CodePermissionDenied
	}
	return CodeUnavailable
}
