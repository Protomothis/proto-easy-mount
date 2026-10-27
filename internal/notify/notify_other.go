//go:build !windows && !darwin

package notify

func show(string, string) {}

// UseAppID does nothing here.
func UseAppID() {}
