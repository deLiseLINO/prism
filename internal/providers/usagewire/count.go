package usagewire

import (
	"encoding/json"
	"errors"
	"math"
)

var ErrCount = errors.New("count must be an exact nonnegative integer within int64")

func Number(value any) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, ErrCount
	}
	return Exact(string(number))
}

func Exact(raw string) (int64, error) {
	if raw == "" {
		return 0, ErrCount
	}
	i := 0
	negative := raw[0] == '-'
	if negative {
		i++
	}
	start := i
	if i == len(raw) || raw[i] < '0' || raw[i] > '9' {
		return 0, ErrCount
	}
	if raw[i] == '0' {
		i++
	} else {
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
	}
	fraction := 0
	if i < len(raw) && raw[i] == '.' {
		i++
		begin := i
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		fraction = i - begin
		if fraction == 0 {
			return 0, ErrCount
		}
	}
	end := i
	exponent := 0
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		i++
		down := false
		if i < len(raw) && (raw[i] == '+' || raw[i] == '-') {
			down = raw[i] == '-'
			i++
		}
		begin := i
		limit := len(raw) + 20
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			if exponent < limit {
				exponent = min(limit, exponent*10+int(raw[i]-'0'))
			}
			i++
		}
		if i == begin {
			return 0, ErrCount
		}
		if down {
			exponent = -exponent
		}
	}
	if i != len(raw) {
		return 0, ErrCount
	}
	first, last := -1, -1
	trailing := 0
	for p := start; p < end; p++ {
		if raw[p] == '.' {
			continue
		}
		if raw[p] != '0' {
			if first < 0 {
				first = p
			}
			last, trailing = p, 0
		} else if first >= 0 {
			trailing++
		}
	}
	if first < 0 {
		return 0, nil
	}
	if negative {
		return 0, ErrCount
	}
	shift := exponent - fraction + trailing
	if shift < 0 || shift > 18 {
		return 0, ErrCount
	}
	var count int64
	for p := first; p <= last; p++ {
		if raw[p] == '.' {
			continue
		}
		digit := int64(raw[p] - '0')
		if count > (math.MaxInt64-digit)/10 {
			return 0, ErrCount
		}
		count = count*10 + digit
	}
	for range shift {
		if count > math.MaxInt64/10 {
			return 0, ErrCount
		}
		count *= 10
	}
	return count, nil
}
