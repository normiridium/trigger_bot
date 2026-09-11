package app

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TestLiveGPTLatexArticlePipeline is an explicit production integration eval.
// It spends OpenAI tokens and briefly sends Telegram messages, so normal test
// runs must never enable it implicitly.
func TestLiveGPTLatexArticlePipeline(t *testing.T) {
	if os.Getenv("RUN_LIVE_GPT_LATEX") != "1" {
		t.Skip("set RUN_LIVE_GPT_LATEX=1 to run the paid live integration eval")
	}

	mongoURI := requireLiveTestEnv(t, "MONGO_URI")
	token := requireLiveTestEnv(t, "TELEGRAM_BOT_TOKEN")
	ownerID, err := strconv.ParseInt(requireLiveTestEnv(t, "OWNER_ID"), 10, 64)
	if err != nil || ownerID == 0 {
		t.Fatalf("invalid OWNER_ID: %v", err)
	}

	store, err := OpenStore(mongoURI)
	if err != nil {
		t.Fatalf("open production store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	trigger, err := store.GetTrigger(9)
	if err != nil {
		t.Fatalf("load Olenyam trigger: %v", err)
	}
	if trigger == nil || trigger.ActionType != ActionTypeGPTPrompt {
		t.Fatalf("trigger 9 is not the Olenyam GPT trigger")
	}
	promptTemplate := pickResponseVariantText(trigger.ResponseText)
	if strings.TrimSpace(promptTemplate) == "" {
		t.Fatal("Olenyam GPT prompt is empty")
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("open Telegram bot: %v", err)
	}
	templateLookup := buildTemplateLookup(store)

	tests := []struct {
		name            string
		request         string
		wantAttachments bool
		wantNative      string
	}{
		{
			name:       "simple-inline-dollar",
			request:    "Оформи короткую статью с заголовком и одним абзацем. Вставь ровно одну формулу $E = mc^2$ внутри предложения. Не добавляй других формул и не помещай формулу в кодовый блок.",
			wantNative: "$E = mc^2$",
		},
		{
			name:       "simple-display-dollar",
			request:    "Оформи короткую статью с заголовком. Отдельной формулой вставь ровно $$a^2 + b^2 = c^2$$. Не добавляй других формул и не используй кодовый блок.",
			wantNative: "$$a^2 + b^2 = c^2$$",
		},
		{
			name:       "simple-inline-parentheses",
			request:    "Оформи короткую статью с заголовком и одним абзацем. Вставь ровно одну формулу \\(x^2 + y^2\\) внутри предложения. Сохрани указанные разделители, не добавляй других формул и не используй кодовый блок.",
			wantNative: "$x^2 + y^2$",
		},
		{
			name:       "simple-display-brackets",
			request:    "Оформи короткую статью с заголовком. Отдельной формулой вставь ровно \\[E = mc^2\\]. Сохрани указанные разделители, не добавляй других формул и не используй кодовый блок.",
			wantNative: "$$E = mc^2$$",
		},
		{
			name:       "native-calculus",
			request:    "Оформи короткую статью с заголовком. Отдельной формулой вставь ровно $$\\int_0^1 x^2\\,dx = \\frac{1}{3}$$. Не добавляй других формул и не используй кодовый блок.",
			wantNative: "$$\\int_0^1 x^2\\,dx = \\frac{1}{3}$$",
		},
		{
			name:            "render-cases",
			request:         "Оформи короткую статью с заголовком. Вставь ровно этот LaTeX-блок: $$f(x)=\\begin{cases}x^2,&x\\ge0\\\\-x,&x<0\\end{cases}$$. Не упрощай его и не помещай в кодовый блок.",
			wantAttachments: true,
		},
		{
			name:            "render-matrix",
			request:         "Оформи короткую статью с заголовком. Вставь ровно этот LaTeX-блок: $$A=\\begin{pmatrix}1&2\\\\3&4\\end{pmatrix}$$. Не упрощай его и не помещай в кодовый блок.",
			wantAttachments: true,
		},
		{
			name:            "render-chemistry",
			request:         "Оформи короткую статью с заголовком. Вставь ровно этот LaTeX-блок: $$\\ce{2H2 + O2 -> 2H2O}$$. Не переписывай реакцию обычным текстом и не помещай в кодовый блок.",
			wantAttachments: true,
		},
		{
			name:            "render-labeled-arrow",
			request:         "Оформи короткую статью с заголовком. Вставь ровно этот LaTeX-блок: $$A \\xrightarrow{k_1} B \\xrightarrow{k_2} C$$. Не заменяй стрелки и не помещай в кодовый блок.",
			wantAttachments: true,
		},
		{
			name:            "mixed-native-and-rendered",
			request:         "Сделай статью: заголовок, два пункта списка и таблицу 2x2. В первом абзаце оставь inline-формулу $x^2$. После таблицы вставь ровно $$\\begin{array}{cc}a&b\\\\c&d\\end{array}$$. Не используй кодовые блоки и не добавляй других формул.",
			wantAttachments: true,
			wantNative:      "$x^2$",
		},
	}

	totalTokens := 0
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tgbotapi.Message{
				MessageID: 900000 + i,
				From: &tgbotapi.User{
					ID:        ownerID,
					FirstName: "Olenka",
				},
				Chat: &tgbotapi.Chat{
					ID:   ownerID,
					Type: "private",
				},
				Text: "Оленям, " + tt.request,
			}
			ctx := newTemplateContext(bot, msg, trigger, templateLookup)
			result, err := generateChatGPTReply(ctx, promptTemplate)
			if err != nil {
				t.Fatalf("generate GPT reply: %v", err)
			}
			totalTokens += result.Usage.TotalTokens
			t.Logf("model output (%d tokens):\n%s", result.Usage.TotalTokens, result.Text)

			if !containsRichArticleMarkup(result.Text) {
				t.Error("model output was not classified as a rich article")
			}
			markdown := responseToRichMarkdownArticle(result.Text)
			markdown = normalizeTelegramLineBreaks(markdown)
			markdown = sanitizeTelegramText(markdown)
			markdown = strings.TrimSpace(markdown)
			rendered, attachments, err := renderRichArticleMediaBlocks(markdown)
			if err != nil {
				t.Fatalf("render production rich article pipeline: %v", err)
			}
			rendered = sanitizeRichArticlePhotoLinks(rendered, attachments)

			if tt.wantAttachments && len(attachments) == 0 {
				t.Error("complex formula was not converted to an attachment")
			}
			if !tt.wantAttachments && len(attachments) != 0 {
				t.Errorf("native formula was converted unnecessarily: %d attachments", len(attachments))
			}
			if tt.wantNative != "" && !strings.Contains(rendered, tt.wantNative) {
				t.Errorf("native formula %q missing after processing:\n%s", tt.wantNative, rendered)
			}
			if strings.Contains(rendered, `\(`) || strings.Contains(rendered, `\[`) {
				t.Errorf("non-Telegram math delimiters survived outside the requested output:\n%s", rendered)
			}

			attachmentInfo := make([]string, 0, len(attachments))
			for _, attachment := range attachments {
				if !isPNGBytes(attachment.PNG) {
					t.Errorf("attachment %s is not PNG", attachment.ID)
					continue
				}
				cfg, err := png.DecodeConfig(bytes.NewReader(attachment.PNG))
				if err != nil {
					t.Errorf("decode attachment %s: %v", attachment.ID, err)
					continue
				}
				attachmentInfo = append(attachmentInfo, fmt.Sprintf("%s=%dx%d/%dB/document=%v", attachment.ID, cfg.Width, cfg.Height, len(attachment.PNG), attachment.Document))
				if attachment.Document {
					t.Errorf("eval attachment unexpectedly requires a separate document: %s", attachment.FileName)
				}
			}
			t.Logf("pipeline: attachments=%d [%s]\nrendered markdown:\n%s", len(attachments), strings.Join(attachmentInfo, ", "), rendered)

			var sent tgbotapi.Message
			if len(attachments) > 0 {
				sent, err = sendRichMarkdownArticleWithMedia(sendContext{Bot: bot, ChatID: ownerID}, rendered, attachments)
			} else {
				sent, err = bot.Send(tgbotapi.NewRichMessageMarkdown(ownerID, rendered))
			}
			if err != nil {
				t.Fatalf("send Telegram rich article: %v", err)
			}
			if sent.MessageID == 0 {
				t.Fatal("Telegram returned an empty message ID")
			}
			if _, err := bot.Request(tgbotapi.NewDeleteMessage(ownerID, sent.MessageID)); err != nil {
				t.Errorf("delete Telegram eval message %d: %v", sent.MessageID, err)
			}
		})
	}
	t.Logf("live LaTeX article eval total tokens: %d", totalTokens)
}

func requireLiveTestEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required for live integration eval", name)
	}
	return value
}

func TestLatexArticleVisualFixtures(t *testing.T) {
	outputDir := strings.TrimSpace(os.Getenv("LATEX_VISUAL_FIXTURE_DIR"))
	if outputDir == "" {
		t.Skip("set LATEX_VISUAL_FIXTURE_DIR to render visual fixtures")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("create visual fixture directory: %v", err)
	}

	fixtures := map[string]string{
		"cases":     `$$f(x)=\begin{cases}x^2,&x\ge0\\-x,&x<0\end{cases}$$`,
		"matrix":    `$$A=\begin{pmatrix}1&2\\3&4\end{pmatrix}$$`,
		"chemistry": `$$\ce{2H2 + O2 -> 2H2O}$$`,
		"arrow":     `$$A \xrightarrow{k_1} B \xrightarrow{k_2} C$$`,
		"array":     `$$\begin{array}{cc}a&b\\c&d\end{array}$$`,
	}
	for name, markdown := range fixtures {
		_, attachments, err := renderRichArticleMediaBlocks(markdown)
		if err != nil {
			t.Fatalf("render %s fixture: %v", name, err)
		}
		if len(attachments) != 1 {
			t.Fatalf("%s fixture produced %d attachments, want 1", name, len(attachments))
		}
		path := filepath.Join(outputDir, name+".png")
		if err := os.WriteFile(path, attachments[0].PNG, 0o600); err != nil {
			t.Fatalf("write %s fixture: %v", name, err)
		}
		t.Logf("wrote %s", path)
	}
}

func TestLiveTelegramModelStyleFormulaList(t *testing.T) {
	if os.Getenv("RUN_LIVE_TELEGRAM_LATEX") != "1" {
		t.Skip("set RUN_LIVE_TELEGRAM_LATEX=1 to test the mixed native/rendered article")
	}
	token := requireLiveTestEnv(t, "TELEGRAM_BOT_TOKEN")
	chatIDText := strings.TrimSpace(os.Getenv("LIVE_TELEGRAM_LATEX_CHAT_ID"))
	if chatIDText == "" {
		chatIDText = requireLiveTestEnv(t, "OWNER_ID")
	}
	chatID, err := strconv.ParseInt(chatIDText, 10, 64)
	if err != nil || chatID == 0 {
		t.Fatalf("invalid Telegram test chat ID: %v", err)
	}
	replyTo := 0
	if raw := strings.TrimSpace(os.Getenv("LIVE_TELEGRAM_LATEX_REPLY_TO")); raw != "" {
		replyTo, err = strconv.Atoi(raw)
		if err != nil || replyTo <= 0 {
			t.Fatalf("invalid LIVE_TELEGRAM_LATEX_REPLY_TO: %v", err)
		}
	}
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("open Telegram bot: %v", err)
	}

	markdown := strings.Join([]string{
		"Оленька, вот тебе большой список формул для теста LaTeX. Вставляй по одной или сразу кучей, если система позволяет.",
		`1. \( E=mc^2 \)`,
		`2. \( a^2 + b^2 = c^2 \)`,
		`3. \( x = \frac{-b \pm \sqrt{b^2-4ac}}{2a} \)`,
		`4. \( \int_{0}^{\infty} e^{-x^2}dx = \frac{\sqrt{\pi}}{2} \)`,
		`5. \( \sum_{n=1}^{\infty} \frac{1}{n^2} = \frac{\pi^2}{6} \)`,
		`6. \( \lim_{x \to 0} \frac{\sin x}{x} = 1 \)`,
		`7. \( \Delta G = \Delta H - T\Delta S \)`,
		`8. \( \vec{F}=q\vec{E} + q\vec{v}\times\vec{B} \)`,
		`9. \( \nabla \cdot \vec{E} = \frac{\rho}{\varepsilon_0} \)`,
		`10. \( \hat{H}\Psi = E\Psi \)`,
		`11. \( dS = \frac{\delta Q}{T} \)`,
		`12. \( pV = nRT \)`,
		`13. \( \mu = \frac{m_1 m_2}{m_1 + m_2} \)`,
		`14. \( \frac{dN}{dt} = -\lambda N \)`,
		`15. \( S = k_B \ln \Omega \)`,
		`16. \( \vec{p} = m\vec{v} \)`,
		`17. \( \oint \vec{B}\cdot d\vec{l} = \mu_0 I \)`,
		`18. \( F = G \frac{m_1 m_2}{r^2} \)`,
		`19. \( \vec{A} = A_x\hat{i} + A_y\hat{j} + A_z\hat{k} \)`,
		`20. \( \langle x \rangle = \int_{-\infty}^{\infty} x |\psi(x)|^2 dx \)`,
		`21. \( y = mx + b \)`,
		`22. \( \frac{\partial f}{\partial x} \)`,
		`23. \( \displaystyle \int \frac{1}{x}dx = \ln|x| + C \)`,
		`24. \( \left(\frac{a}{b}\right)^n = a^n b^{-n} \)`,
		`25. \( [A,B] = AB - BA \)`,
		`26. \( e^{i\pi} + 1 = 0 \)`,
		`27. \( \alpha, \beta, \gamma, \delta, \epsilon \)`,
		`28. \( \left| \begin{matrix} a & b \\ c & d \end{matrix} \right| = ad-bc \)`,
		`29. \( \cos^2 x + \sin^2 x = 1 \)`,
		`30. \( \overrightarrow{AB} = \vec{b}-\vec{a} \)`,
		`31. \( \max(x_1, x_2, ..., x_n) \)`,
		`32. \( \forall x \in \mathbb{R} \)`,
		`33. \( \sqrt[n]{a} \)`,
		`34. \( f(x) \xrightarrow{x \to a} L \)`,
		`35. \( \mathbb{N}, \mathbb{R}, \mathbb{C}, \mathbb{Z} \)`,
	}, "\n")
	rendered, attachments, err := renderRichArticleMediaBlocks(markdown)
	if err != nil {
		t.Fatalf("render model-style formula list: %v", err)
	}
	if len(attachments) != 3 {
		t.Fatalf("got %d attachments, want 3", len(attachments))
	}
	rendered = sanitizeRichArticlePhotoLinks(rendered, attachments)
	sent, err := sendRichMarkdownArticleWithMedia(sendContext{Bot: bot, ChatID: chatID, ReplyTo: replyTo}, rendered, attachments)
	if err != nil {
		t.Fatalf("send mixed formula article: %v", err)
	}
	if sent.MessageID == 0 {
		t.Fatal("Telegram returned an empty message ID")
	}
	t.Logf("sent Telegram rich article chat=%d message=%d reply_to=%d", chatID, sent.MessageID, replyTo)
	if os.Getenv("LIVE_TELEGRAM_LATEX_KEEP") != "1" {
		if _, err := bot.Request(tgbotapi.NewDeleteMessage(chatID, sent.MessageID)); err != nil {
			t.Fatalf("delete Telegram test message %d: %v", sent.MessageID, err)
		}
	}
}
