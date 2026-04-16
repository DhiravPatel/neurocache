package store

import "strconv"

func fmtItoa(n int64) string              { return strconv.FormatInt(n, 10) }
func fmtSscanInt(s string, out *int64) (int, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	*out = n
	return 1, nil
}
