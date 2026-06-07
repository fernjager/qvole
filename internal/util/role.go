package util

func RoleString(isServer bool) string {
	if isServer {
		return "server"
	}
	return "client"
}
