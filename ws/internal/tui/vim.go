package tui

type vimMotion int

const (
	motionNone vimMotion = iota
	motionDown
	motionUp
	motionHalfDown
	motionHalfUp
	motionPageDown
	motionPageUp
	motionTop
	motionBottom
)

const defaultMotionPage = 16

func classifyVimMotion(key string) vimMotion {
	switch key {
	case "j", "down":
		return motionDown
	case "k", "up":
		return motionUp
	case "ctrl+d":
		return motionHalfDown
	case "ctrl+u":
		return motionHalfUp
	case "ctrl+f", "pgdown":
		return motionPageDown
	case "ctrl+b", "pgup":
		return motionPageUp
	case "home":
		return motionTop
	case "G", "end":
		return motionBottom
	default:
		return motionNone
	}
}

func applyListMotion(cursor, total, pageSize int, m vimMotion) (int, bool) {
	if total <= 0 {
		return 0, false
	}
	if pageSize <= 0 {
		pageSize = defaultMotionPage
	}
	half := max(4, pageSize/2)
	full := max(8, pageSize)

	next := clamp(cursor, 0, total-1)
	switch m {
	case motionDown:
		next = min(total-1, next+1)
	case motionUp:
		next = max(0, next-1)
	case motionHalfDown:
		next = min(total-1, next+half)
	case motionHalfUp:
		next = max(0, next-half)
	case motionPageDown:
		next = min(total-1, next+full)
	case motionPageUp:
		next = max(0, next-full)
	case motionTop:
		next = 0
	case motionBottom:
		next = total - 1
	default:
		return cursor, false
	}
	return next, next != cursor
}

func applyScrollMotion(scroll, maxScroll, pageSize int, m vimMotion) (int, bool) {
	if pageSize <= 0 {
		pageSize = defaultMotionPage
	}
	half := max(4, pageSize/2)
	full := max(8, pageSize)

	cur := max(0, scroll)
	next := cur
	switch m {
	case motionDown:
		next = cur + 1
	case motionUp:
		next = max(0, cur-1)
	case motionHalfDown:
		next = cur + half
	case motionHalfUp:
		next = max(0, cur-half)
	case motionPageDown:
		next = cur + full
	case motionPageUp:
		next = max(0, cur-full)
	case motionTop:
		next = 0
	case motionBottom:
		if maxScroll > 0 {
			next = maxScroll
		} else {
			next = 1 << 20
		}
	default:
		return scroll, false
	}
	if maxScroll > 0 && next > maxScroll {
		next = maxScroll
	}
	return next, next != cur
}
