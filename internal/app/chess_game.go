package app

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const chessInitialFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

var chessMoveRe = regexp.MustCompile(`(?i)^\s*([a-h][1-8])\s*[-:]\s*([a-h][1-8])\s*$`)

const (
	chessWhiteBottomImageIDPrefix = "chess_wb_"
	chessWhiteTopImageIDPrefix    = "chess_wt_"
)

type chessPosition struct {
	Board    [8][8]rune
	Side     rune
	Castling string
	EP       string
	Halfmove int
	Fullmove int
}

type chessMove struct {
	From       string
	To         string
	Piece      rune
	DisambFile byte
	DisambRank byte
	CastleSide string
	Notation   string
}

type chessArticleState struct {
	FEN         string
	WhiteBottom bool
}

func handleChessCommand(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) bool {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return false
	}
	whiteBottom := rand.Intn(2) == 0
	if _, err := sendChessArticle(sendContext{Bot: bot, ChatID: msg.Chat.ID}, chessInitialFEN, whiteBottom, "старт", chessUserLabel(msg.From)); err != nil {
		log.Printf("chess send initial failed chat=%d msg=%d err=%v", msg.Chat.ID, msg.MessageID, err)
		reportChatFailure(bot, msg.Chat.ID, "ошибка шахматной доски", err)
		return true
	}
	deleteTelegramMessageQuiet(bot, msg.Chat.ID, msg.MessageID, "chess command source")
	return true
}

func handleChessMoveReply(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, rawMsg *rawMessageWithEmoji) bool {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil || msg.ReplyToMessage == nil {
		return false
	}
	if msg.ReplyToMessage.From == nil || !msg.ReplyToMessage.From.IsBot || msg.ReplyToMessage.From.ID != bot.Self.ID {
		return false
	}
	move, ok := parseChessMoveText(firstNonEmptyUserText(msg))
	if !ok {
		return false
	}
	state, err := parseChessArticleStateFromReply(msg.ReplyToMessage, rawMsg)
	if err != nil {
		log.Printf("chess move ignored: reply has no chess state chat=%d msg=%d reply=%d err=%v",
			msg.Chat.ID, msg.MessageID, msg.ReplyToMessage.MessageID, err)
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Не нашла FEN в статье-реплае. Ответь ходом на шахматную статью.", false)
		return true
	}
	pos, err := parseChessPositionFEN(state.FEN)
	if err != nil {
		log.Printf("chess move parse FEN failed chat=%d msg=%d fen=%q err=%v", msg.Chat.ID, msg.MessageID, state.FEN, err)
		reportChatFailure(bot, msg.Chat.ID, "ошибка FEN в шахматной статье", err)
		return true
	}
	resolvedMove, err := resolveChessMove(pos, move)
	if err != nil {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Ход не применён: "+clipText(err.Error(), 200), false)
		return true
	}
	next, err := applyChessMove(pos, resolvedMove)
	if err != nil {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Ход не применён: "+clipText(err.Error(), 200), false)
		return true
	}
	if _, err := sendChessArticle(sendContext{Bot: bot, ChatID: msg.Chat.ID}, next.FEN(), state.WhiteBottom, chessMoveDisplay(move, resolvedMove), chessUserLabel(msg.From)); err != nil {
		log.Printf("chess send move failed chat=%d msg=%d move=%s-%s err=%v", msg.Chat.ID, msg.MessageID, resolvedMove.From, resolvedMove.To, err)
		reportChatFailure(bot, msg.Chat.ID, "ошибка шахматной доски", err)
		return true
	}
	deleteTelegramMessageQuiet(bot, msg.Chat.ID, msg.MessageID, "chess move source")
	return true
}

func sendChessArticle(ctx sendContext, fen string, whiteBottom bool, moveText, userLabel string) (tgbotapi.Message, error) {
	pngBytes, err := renderRichArticleFENBoardPNGWithOrientation(fen, whiteBottom)
	if err != nil {
		return tgbotapi.Message{}, err
	}
	id := chessArticleImageID(fen, whiteBottom, moveText, userLabel)
	attachment, err := newRichArticleRenderedAttachment(id, pngBytes)
	if err != nil {
		return tgbotapi.Message{}, err
	}
	markdown := renderChessArticleMarkdown(attachment, fen, moveText, userLabel)
	sent, err := sendRichMarkdownArticleWithMedia(ctx, markdown, []richArticleRenderedAttachment{attachment})
	if err != nil {
		return sent, err
	}
	addOutgoingChatRecentMessage(ctx.ChatID, markdown)
	return sent, nil
}

func renderChessArticleMarkdown(attachment richArticleRenderedAttachment, fen string, moveText, userLabel string) string {
	moveText = strings.TrimSpace(moveText)
	if moveText == "" {
		moveText = "старт"
	}
	userLabel = strings.TrimSpace(userLabel)
	if userLabel == "" {
		userLabel = "игрок"
	}
	side := "белые"
	if pos, err := parseChessPositionFEN(fen); err == nil && pos.Side == 'b' {
		side = "чёрные"
	}
	return strings.Join([]string{
		richArticleRenderedFormulaMarkdown(attachment.ID),
		"",
		richArticleInlineCodeLiteral(strings.TrimSpace(fen)),
		"Ход: " + moveText + " (" + userLabel + ")",
		"Очередь: " + side,
	}, "\n")
}

func chessArticleImageID(fen string, whiteBottom bool, moveText, userLabel string) string {
	prefix := chessWhiteBottomImageIDPrefix
	if !whiteBottom {
		prefix = chessWhiteTopImageIDPrefix
	}
	return prefix + richArticleRenderedFenceID("chess", fen+"\n"+strconv.FormatBool(whiteBottom)+"\n"+moveText+"\n"+userLabel, 1, "")
}

func parseChessArticleStateFromReply(replyMsg *tgbotapi.Message, rawMsg *rawMessageWithEmoji) (chessArticleState, error) {
	st, err := parseChessArticleState(firstNonEmptyUserText(replyMsg))
	if rawMsg != nil && rawMsg.ReplyToMessage != nil {
		if whiteBottom, ok := extractChessWhiteBottomMarker(string(rawMsg.ReplyToMessage.RichMessage)); ok {
			st.WhiteBottom = whiteBottom
		}
	}
	return st, err
}

func parseChessArticleState(text string) (chessArticleState, error) {
	var st chessArticleState
	st.WhiteBottom = true
	if whiteBottom, ok := extractChessWhiteBottomMarker(text); ok {
		st.WhiteBottom = whiteBottom
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "fen:") {
			st.FEN = strings.TrimSpace(line[len("fen:"):])
			st.FEN = strings.Trim(st.FEN, "` \t")
		}
		if strings.HasPrefix(lower, "белые:") {
			body := strings.TrimSpace(lower[len("белые:"):])
			if strings.Contains(body, "сверху") {
				st.WhiteBottom = false
			} else if strings.Contains(body, "снизу") {
				st.WhiteBottom = true
			}
		}
	}
	if strings.TrimSpace(st.FEN) == "" {
		st.FEN = extractChessFENFromText(text)
	}
	if strings.TrimSpace(st.FEN) == "" {
		return st, errors.New("FEN line not found")
	}
	if _, err := parseChessPositionFEN(st.FEN); err != nil {
		return st, err
	}
	return st, nil
}

func extractChessWhiteBottomMarker(text string) (bool, bool) {
	switch {
	case strings.Contains(text, chessWhiteTopImageIDPrefix):
		return false, true
	case strings.Contains(text, chessWhiteBottomImageIDPrefix):
		return true, true
	default:
		return false, false
	}
}

func extractChessFENFromText(text string) string {
	fields := strings.Fields(normalizeRawRichText(text))
	for i := range fields {
		start := cleanChessFENToken(fields[i])
		start = trimChessFENLabel(start)
		if start == "" || !strings.Contains(start, "/") {
			continue
		}
		best := ""
		for j := i; j < len(fields) && j < i+6; j++ {
			candidateFields := make([]string, 0, j-i+1)
			for k := i; k <= j; k++ {
				token := cleanChessFENToken(fields[k])
				if k == i {
					token = trimChessFENLabel(token)
				}
				if token == "" {
					continue
				}
				candidateFields = append(candidateFields, token)
			}
			candidate := strings.Join(candidateFields, " ")
			if _, err := parseRichArticleFEN(candidate); err == nil {
				best = candidate
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

func cleanChessFENToken(s string) string {
	return strings.Trim(s, "`'\"“”‘’.,;()[]{}<>")
}

func trimChessFENLabel(s string) string {
	if len(s) >= len("fen:") && strings.EqualFold(s[:len("fen:")], "fen:") {
		return strings.TrimSpace(s[len("fen:"):])
	}
	return s
}

func parseChessMoveText(text string) (chessMove, bool) {
	text = strings.TrimSpace(text)
	if move, ok := parseChessCastlingMoveText(text); ok {
		return move, true
	}
	m := chessMoveRe.FindStringSubmatch(text)
	if len(m) != 3 {
		return parseChessAlgebraicMoveText(text)
	}
	from := strings.ToLower(m[1])
	to := strings.ToLower(m[2])
	return chessMove{From: from, To: to, Notation: from + "-" + to}, true
}

func parseChessCastlingMoveText(text string) (chessMove, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return chessMove{}, false
	}
	s = strings.TrimRight(s, "+#!?")
	s = strings.NewReplacer("О", "O", "о", "O", "0", "O", "–", "-", "—", "-").Replace(s)
	switch strings.ToUpper(s) {
	case "O-O":
		return chessMove{Piece: 'K', CastleSide: "king", Notation: text}, true
	case "O-O-O":
		return chessMove{Piece: 'K', CastleSide: "queen", Notation: text}, true
	default:
		return chessMove{}, false
	}
}

func parseChessAlgebraicMoveText(text string) (chessMove, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return chessMove{}, false
	}
	s = strings.TrimRight(s, "+#!?")
	piece, rest, ok := parseChessAlgebraicPiece(s)
	if !ok {
		return chessMove{}, false
	}
	rest = strings.NewReplacer("x", "", "X", "", "х", "", "Х", "", "×", "", ":", "").Replace(strings.TrimSpace(rest))
	if len(rest) < 2 {
		return chessMove{}, false
	}
	target := strings.ToLower(rest[len(rest)-2:])
	if _, _, ok := chessSquareIndex(target); !ok {
		return chessMove{}, false
	}
	disamb := strings.ToLower(rest[:len(rest)-2])
	if len(disamb) > 2 {
		return chessMove{}, false
	}
	mv := chessMove{To: target, Piece: piece, Notation: s}
	for i := 0; i < len(disamb); i++ {
		ch := disamb[i]
		switch {
		case ch >= 'a' && ch <= 'h':
			if mv.DisambFile != 0 {
				return chessMove{}, false
			}
			mv.DisambFile = ch
		case ch >= '1' && ch <= '8':
			if mv.DisambRank != 0 {
				return chessMove{}, false
			}
			mv.DisambRank = ch
		default:
			return chessMove{}, false
		}
	}
	return mv, true
}

func parseChessAlgebraicPiece(s string) (rune, string, bool) {
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "кр") {
		return 'K', s[len("кр"):], true
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 0 {
		return 0, "", false
	}
	switch r {
	case 'К', 'к':
		return 'N', s[size:], true
	case 'Ф', 'ф':
		return 'Q', s[size:], true
	case 'Л', 'л':
		return 'R', s[size:], true
	case 'С', 'с':
		return 'B', s[size:], true
	case 'N', 'n':
		return 'N', s[size:], true
	case 'B', 'b':
		return 'B', s[size:], true
	case 'R', 'r':
		return 'R', s[size:], true
	case 'Q', 'q':
		return 'Q', s[size:], true
	case 'K', 'k':
		return 'K', s[size:], true
	default:
		if len(s) >= 2 {
			target := strings.ToLower(s[len(s)-2:])
			if _, _, ok := chessSquareIndex(target); ok {
				return 'P', s, true
			}
		}
		return 0, "", false
	}
}

func parseChessPositionFEN(fen string) (chessPosition, error) {
	var pos chessPosition
	if _, err := parseRichArticleFEN(fen); err != nil {
		return pos, err
	}
	fields := strings.Fields(strings.TrimSpace(fen))
	if len(fields) == 0 {
		return pos, errors.New("empty FEN")
	}
	ranks := strings.Split(fields[0], "/")
	for r, row := range ranks {
		file := 0
		for _, ch := range row {
			if ch >= '1' && ch <= '8' {
				file += int(ch - '0')
				continue
			}
			pos.Board[r][file] = ch
			file++
		}
	}
	pos.Side = 'w'
	if len(fields) >= 2 && fields[1] == "b" {
		pos.Side = 'b'
	}
	pos.Castling = "-"
	if len(fields) >= 3 {
		pos.Castling = fields[2]
	}
	pos.EP = "-"
	if len(fields) >= 4 {
		pos.EP = fields[3]
	}
	if len(fields) >= 5 {
		pos.Halfmove, _ = strconv.Atoi(fields[4])
	}
	pos.Fullmove = 1
	if len(fields) >= 6 {
		pos.Fullmove, _ = strconv.Atoi(fields[5])
	}
	if pos.Fullmove <= 0 {
		pos.Fullmove = 1
	}
	return pos, nil
}

func applyChessMove(pos chessPosition, mv chessMove) (chessPosition, error) {
	if strings.TrimSpace(mv.From) == "" {
		var err error
		mv, err = resolveChessMove(pos, mv)
		if err != nil {
			return pos, err
		}
	}
	fromR, fromC, ok := chessSquareIndex(mv.From)
	if !ok {
		return pos, fmt.Errorf("неверная клетка %q", mv.From)
	}
	toR, toC, ok := chessSquareIndex(mv.To)
	if !ok {
		return pos, fmt.Errorf("неверная клетка %q", mv.To)
	}
	piece := pos.Board[fromR][fromC]
	if piece == 0 {
		return pos, fmt.Errorf("на %s нет фигуры", mv.From)
	}
	if chessPieceSide(piece) != pos.Side {
		return pos, fmt.Errorf("сейчас ходят %s", chessSideName(pos.Side))
	}
	target := pos.Board[toR][toC]
	if target != 0 && chessPieceSide(target) == pos.Side {
		return pos, fmt.Errorf("на %s стоит своя фигура", mv.To)
	}
	if !chessMovePseudoLegal(pos, piece, fromR, fromC, toR, toC) {
		return pos, fmt.Errorf("ход %s-%s не похож на легальный для этой фигуры", mv.From, mv.To)
	}

	next := pos
	next.Board[fromR][fromC] = 0
	enPassantCapture := strings.EqualFold(mv.To, pos.EP) && (piece == 'P' || piece == 'p') && target == 0 && fromC != toC
	if enPassantCapture {
		capR := toR + 1
		if piece == 'p' {
			capR = toR - 1
		}
		if capR >= 0 && capR < 8 {
			next.Board[capR][toC] = 0
		}
	}
	next.Board[toR][toC] = piece
	if piece == 'P' && toR == 0 {
		next.Board[toR][toC] = 'Q'
	}
	if piece == 'p' && toR == 7 {
		next.Board[toR][toC] = 'q'
	}
	if (piece == 'K' || piece == 'k') && absInt(toC-fromC) == 2 {
		next = applyChessCastlingRook(next, piece, toR, toC)
	}
	next.Castling = updateChessCastlingRights(pos.Castling, piece, fromR, fromC, target, toR, toC)
	next.EP = "-"
	if (piece == 'P' || piece == 'p') && absInt(toR-fromR) == 2 {
		next.EP = chessSquareName((fromR+toR)/2, fromC)
	}
	if piece == 'P' || piece == 'p' || target != 0 || enPassantCapture {
		next.Halfmove = 0
	} else {
		next.Halfmove++
	}
	if pos.Side == 'b' {
		next.Fullmove++
	}
	if pos.Side == 'w' {
		next.Side = 'b'
	} else {
		next.Side = 'w'
	}
	return next, nil
}

func resolveChessMove(pos chessPosition, mv chessMove) (chessMove, error) {
	if strings.TrimSpace(mv.From) != "" {
		return mv, nil
	}
	if mv.CastleSide != "" {
		return resolveChessCastlingMove(pos, mv)
	}
	toR, toC, ok := chessSquareIndex(mv.To)
	if !ok {
		return mv, fmt.Errorf("неверная клетка %q", mv.To)
	}
	if mv.Piece == 0 {
		return mv, fmt.Errorf("не указана фигура для хода %q", mv.Notation)
	}
	var candidates []string
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			piece := pos.Board[r][c]
			if piece == 0 || chessPieceSide(piece) != pos.Side {
				continue
			}
			if chessPieceKind(piece) != mv.Piece {
				continue
			}
			from := chessSquareName(r, c)
			if mv.DisambFile != 0 && from[0] != mv.DisambFile {
				continue
			}
			if mv.DisambRank != 0 && from[1] != mv.DisambRank {
				continue
			}
			target := pos.Board[toR][toC]
			if target != 0 && chessPieceSide(target) == pos.Side {
				continue
			}
			if chessMovePseudoLegal(pos, piece, r, c, toR, toC) {
				candidates = append(candidates, from)
			}
		}
	}
	switch len(candidates) {
	case 0:
		return mv, fmt.Errorf("не нашла фигуру для хода %s", chessMoveDisplay(mv, mv))
	case 1:
		mv.From = candidates[0]
		return mv, nil
	default:
		return mv, fmt.Errorf("неоднозначный ход %s: уточни начальную клетку", chessMoveDisplay(mv, mv))
	}
}

func resolveChessCastlingMove(pos chessPosition, mv chessMove) (chessMove, error) {
	switch pos.Side {
	case 'w':
		mv.From = "e1"
		if mv.CastleSide == "queen" {
			mv.To = "c1"
		} else {
			mv.To = "g1"
		}
	case 'b':
		mv.From = "e8"
		if mv.CastleSide == "queen" {
			mv.To = "c8"
		} else {
			mv.To = "g8"
		}
	default:
		return mv, fmt.Errorf("неверная сторона хода для рокировки")
	}
	return mv, nil
}

func chessMoveDisplay(original, resolved chessMove) string {
	if strings.TrimSpace(original.Notation) != "" && strings.TrimSpace(original.From) == "" {
		return strings.TrimSpace(original.Notation)
	}
	if resolved.From != "" && resolved.To != "" {
		return resolved.From + "-" + resolved.To
	}
	if original.From != "" && original.To != "" {
		return original.From + "-" + original.To
	}
	return strings.TrimSpace(original.Notation)
}

func chessMovePseudoLegal(pos chessPosition, piece rune, fromR, fromC, toR, toC int) bool {
	dr := toR - fromR
	dc := toC - fromC
	target := pos.Board[toR][toC]
	switch piece {
	case 'P', 'p':
		dir := -1
		start := 6
		if piece == 'p' {
			dir = 1
			start = 1
		}
		if dc == 0 && dr == dir && target == 0 {
			return true
		}
		if dc == 0 && fromR == start && dr == 2*dir && target == 0 && pos.Board[fromR+dir][fromC] == 0 {
			return true
		}
		if absInt(dc) == 1 && dr == dir {
			if target != 0 && chessPieceSide(target) != chessPieceSide(piece) {
				return true
			}
			return strings.EqualFold(chessSquareName(toR, toC), pos.EP)
		}
		return false
	case 'N', 'n':
		return (absInt(dr) == 2 && absInt(dc) == 1) || (absInt(dr) == 1 && absInt(dc) == 2)
	case 'B', 'b':
		return absInt(dr) == absInt(dc) && chessPathClear(pos, fromR, fromC, toR, toC)
	case 'R', 'r':
		return (dr == 0 || dc == 0) && chessPathClear(pos, fromR, fromC, toR, toC)
	case 'Q', 'q':
		return (absInt(dr) == absInt(dc) || dr == 0 || dc == 0) && chessPathClear(pos, fromR, fromC, toR, toC)
	case 'K', 'k':
		if absInt(dr) <= 1 && absInt(dc) <= 1 {
			return true
		}
		if dr == 0 && absInt(dc) == 2 {
			return chessCanCastle(pos, piece, fromR, fromC, toC)
		}
	}
	return false
}

func chessCanCastle(pos chessPosition, piece rune, fromR, fromC, toC int) bool {
	if piece == 'K' && fromR == 7 && fromC == 4 {
		if toC == 6 && strings.Contains(pos.Castling, "K") {
			return pos.Board[7][5] == 0 && pos.Board[7][6] == 0 && pos.Board[7][7] == 'R'
		}
		if toC == 2 && strings.Contains(pos.Castling, "Q") {
			return pos.Board[7][1] == 0 && pos.Board[7][2] == 0 && pos.Board[7][3] == 0 && pos.Board[7][0] == 'R'
		}
	}
	if piece == 'k' && fromR == 0 && fromC == 4 {
		if toC == 6 && strings.Contains(pos.Castling, "k") {
			return pos.Board[0][5] == 0 && pos.Board[0][6] == 0 && pos.Board[0][7] == 'r'
		}
		if toC == 2 && strings.Contains(pos.Castling, "q") {
			return pos.Board[0][1] == 0 && pos.Board[0][2] == 0 && pos.Board[0][3] == 0 && pos.Board[0][0] == 'r'
		}
	}
	return false
}

func applyChessCastlingRook(pos chessPosition, king rune, row, kingToC int) chessPosition {
	if kingToC == 6 {
		pos.Board[row][5] = pos.Board[row][7]
		pos.Board[row][7] = 0
	} else if kingToC == 2 {
		pos.Board[row][3] = pos.Board[row][0]
		pos.Board[row][0] = 0
	}
	_ = king
	return pos
}

func chessPathClear(pos chessPosition, fromR, fromC, toR, toC int) bool {
	stepR := signInt(toR - fromR)
	stepC := signInt(toC - fromC)
	r := fromR + stepR
	c := fromC + stepC
	for r != toR || c != toC {
		if pos.Board[r][c] != 0 {
			return false
		}
		r += stepR
		c += stepC
	}
	return true
}

func updateChessCastlingRights(castling string, piece rune, fromR, fromC int, captured rune, toR, toC int) string {
	rights := map[rune]bool{}
	for _, ch := range castling {
		if ch == 'K' || ch == 'Q' || ch == 'k' || ch == 'q' {
			rights[ch] = true
		}
	}
	remove := func(ch rune) { delete(rights, ch) }
	switch piece {
	case 'K':
		remove('K')
		remove('Q')
	case 'k':
		remove('k')
		remove('q')
	case 'R':
		if fromR == 7 && fromC == 0 {
			remove('Q')
		}
		if fromR == 7 && fromC == 7 {
			remove('K')
		}
	case 'r':
		if fromR == 0 && fromC == 0 {
			remove('q')
		}
		if fromR == 0 && fromC == 7 {
			remove('k')
		}
	}
	if captured == 'R' {
		if toR == 7 && toC == 0 {
			remove('Q')
		}
		if toR == 7 && toC == 7 {
			remove('K')
		}
	}
	if captured == 'r' {
		if toR == 0 && toC == 0 {
			remove('q')
		}
		if toR == 0 && toC == 7 {
			remove('k')
		}
	}
	var b strings.Builder
	for _, ch := range []rune{'K', 'Q', 'k', 'q'} {
		if rights[ch] {
			b.WriteRune(ch)
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func (pos chessPosition) FEN() string {
	var b strings.Builder
	for r := 0; r < 8; r++ {
		if r > 0 {
			b.WriteByte('/')
		}
		empty := 0
		for c := 0; c < 8; c++ {
			piece := pos.Board[r][c]
			if piece == 0 {
				empty++
				continue
			}
			if empty > 0 {
				b.WriteString(strconv.Itoa(empty))
				empty = 0
			}
			b.WriteRune(piece)
		}
		if empty > 0 {
			b.WriteString(strconv.Itoa(empty))
		}
	}
	side := "w"
	if pos.Side == 'b' {
		side = "b"
	}
	castling := strings.TrimSpace(pos.Castling)
	if castling == "" {
		castling = "-"
	}
	ep := strings.TrimSpace(pos.EP)
	if ep == "" {
		ep = "-"
	}
	return fmt.Sprintf("%s %s %s %s %d %d", b.String(), side, castling, ep, pos.Halfmove, pos.Fullmove)
}

func chessSquareIndex(s string) (rank, file int, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 2 {
		return 0, 0, false
	}
	if s[0] < 'a' || s[0] > 'h' || s[1] < '1' || s[1] > '8' {
		return 0, 0, false
	}
	file = int(s[0] - 'a')
	rank = 8 - int(s[1]-'0')
	return rank, file, true
}

func chessSquareName(rank, file int) string {
	return string([]byte{byte('a' + file), byte('8' - rank)})
}

func chessPieceSide(piece rune) rune {
	if piece >= 'A' && piece <= 'Z' {
		return 'w'
	}
	return 'b'
}

func chessPieceKind(piece rune) rune {
	if piece >= 'a' && piece <= 'z' {
		return piece - ('a' - 'A')
	}
	return piece
}

func chessSideName(side rune) string {
	if side == 'b' {
		return "чёрные"
	}
	return "белые"
}

func chessUserLabel(user *tgbotapi.User) string {
	if user == nil {
		return "игрок"
	}
	if username := strings.TrimSpace(user.UserName); username != "" {
		return "@" + username
	}
	if display := strings.TrimSpace(buildUserDisplayName(user)); display != "" {
		return display
	}
	return "игрок"
}

func deleteTelegramMessageQuiet(bot *tgbotapi.BotAPI, chatID int64, msgID int, what string) {
	if bot == nil || chatID == 0 || msgID == 0 {
		return
	}
	if _, err := bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: msgID}); err != nil && debugTriggerLogEnabled {
		log.Printf("delete %s failed chat=%d msg=%d err=%v", what, chatID, msgID, err)
	}
}

func signInt(v int) int {
	if v < 0 {
		return -1
	}
	if v > 0 {
		return 1
	}
	return 0
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
