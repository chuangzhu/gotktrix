package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"mime"
	"strings"
	"time"

	"github.com/chanbakjsd/gotrix/event"
	"github.com/chanbakjsd/gotrix/matrix"
	"github.com/diamondburned/gotk4/pkg/core/gioutil"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotktrix/internal/app"
	"github.com/diamondburned/gotktrix/internal/app/messageview/compose/autocomplete"
	"github.com/diamondburned/gotktrix/internal/app/messageview/message/mauthor"
	"github.com/diamondburned/gotktrix/internal/gotktrix"
	"github.com/diamondburned/gotktrix/internal/gtkutil/cssutil"
	"github.com/diamondburned/gotktrix/internal/gtkutil/imgutil"
	"github.com/diamondburned/gotktrix/internal/md"
	"github.com/pkg/errors"
)

// Input is the input component of the message composer.
type Input struct {
	*gtk.TextView
	buffer *gtk.TextBuffer

	ctx    context.Context
	ctrl   Controller
	roomID matrix.RoomID

	replyingTo matrix.EventID
}

var inputCSS = cssutil.Applier("composer-input", `
	.composer-input,
	.composer-input text {
		background-color: inherit;
	}
	.composer-input {
		padding: 12px 2px;
		padding-bottom: 0;
		margin-top: 10px;
	}
`)

var sendCSS = cssutil.Applier("composer-send", `
	.composer-send {
		margin:   0px;
		padding: 10px;
		border-radius: 0;
	}
`)

func customEmojiHTML(emoji autocomplete.EmojiData) string {
	if emoji.Unicode != "" {
		return emoji.Unicode
	}

	return fmt.Sprintf(
		`<img alt="%s" title="%[1]s" width="32" height="32" src="%s" data-mxc-emoticon/>`,
		html.EscapeString(string(emoji.Name)),
		html.EscapeString(string(emoji.Custom.URL)),
	)
}

const (
	inlineEmojiSize = 18

	sendIcon  = "document-send-symbolic"
	replyIcon = "mail-reply-sender-symbolic"
)

// NewInput creates a new Input instance.
func NewInput(ctx context.Context, ctrl Controller, roomID matrix.RoomID) *Input {
	go requestAllMembers(ctx, roomID)

	tview := gtk.NewTextView()
	tview.SetWrapMode(gtk.WrapWordChar)
	tview.SetAcceptsTab(true)
	tview.SetHExpand(true)
	tview.SetInputHints(0 |
		gtk.InputHintEmoji |
		gtk.InputHintSpellcheck |
		gtk.InputHintWordCompletion |
		gtk.InputHintUppercaseSentences,
	)
	inputCSS(tview)

	buffer := tview.Buffer()

	ac := autocomplete.New(tview, func(row autocomplete.SelectedData) bool {
		return finishAutocomplete(ctx, tview, buffer, row)
	})
	ac.SetTimeout(time.Second)
	ac.Use(
		autocomplete.NewRoomMemberSearcher(ctx, roomID), // @
		autocomplete.NewEmojiSearcher(ctx, roomID),      // :
	)

	// Ugh. We have to be EXTREMELY careful with this context, because if it's
	// misused, it will put the input buffer into a very inconsistent state.
	// It must be invalidated every time to buffer changes, because we don't
	// want to risk

	buffer.Connect("changed", func(buffer *gtk.TextBuffer) {
		md.WYSIWYG(ctx, buffer)
		ac.Autocomplete(ctx)
	})

	enterKeyer := gtk.NewEventControllerKey()
	tview.AddController(enterKeyer)

	tview.Connect("paste-clipboard", func() {
		display := gdk.DisplayGetDefault()

		clipboard := display.Clipboard()
		clipboard.ReadAsync(ctx, clipboard.Formats().MIMETypes(), 0, func(res gio.AsyncResulter) {
			typ, stream, err := clipboard.ReadFinish(res)
			if err != nil {
				app.Error(ctx, errors.Wrap(err, "failed to read clipboard"))
				return
			}

			mime, _, err := mime.ParseMediaType(typ)
			if err != nil {
				app.Error(ctx, errors.Wrapf(err, "clipboard contains invalid MIME %q", typ))
				return
			}

			if strings.HasPrefix(mime, "text") {
				// Ignore texts.
				stream.Close(ctx)
				return
			}

			promptUpload(ctx, roomID, uploadingFile{
				input:  stream,
				reader: gioutil.Reader(ctx, stream),
				mime:   mime,
				name:   "clipboard",
			})
		})
	})

	input := Input{
		TextView: tview,
		buffer:   buffer,
		ctx:      ctx,
		ctrl:     ctrl,
		roomID:   roomID,
	}

	enterKeyer.Connect(
		"key-pressed",
		func(_ *gtk.EventControllerKey, val, code uint, state gdk.ModifierType) bool {
			switch val {
			case gdk.KEY_Return:
				if ac.Select() {
					return true
				}

				// TODO: find a better way to do this. goldmark won't try to
				// parse an incomplete codeblock (I think), but the changed
				// signal will be fired after this signal.
				//
				// Perhaps we could use the FindChar method to avoid allocating
				// a new string (twice) on each keypress.
				head := buffer.StartIter()
				tail := buffer.IterAtOffset(buffer.ObjectProperty("cursor-position").(int))
				uinput := buffer.Text(&head, &tail, false)

				withinCodeblock := strings.Count(uinput, "```")%2 != 0

				// Enter (without holding Shift) sends the message.
				if !state.Has(gdk.ShiftMask) && !withinCodeblock {
					return input.Send()
				}
			case gdk.KEY_Tab:
				return ac.Select()
			case gdk.KEY_Escape:
				return ac.Clear()
			case gdk.KEY_Up:
				return ac.MoveUp()
			case gdk.KEY_Down:
				return ac.MoveDown()
			}

			return false
		},
	)

	return &input
}

// Send sends the message inside the input off.
func (i *Input) Send() bool {
	ev, ok := i.put()
	if !ok {
		return false
	}

	go func() {
		client := gotktrix.FromContext(i.ctx)
		_, err := client.RoomEventSend(ev.RoomID, ev.Type(), ev)
		if err != nil {
			app.Error(i.ctx, errors.Wrap(err, "failed to send message"))
		}
	}()

	head := i.buffer.StartIter()
	tail := i.buffer.EndIter()
	i.buffer.Delete(&head, &tail)

	// Call the controller's ReplyTo method and expect it to rebubble it
	// up to us.
	i.ctrl.ReplyTo("")
	return true
}

// put steals the buffer and puts it into a message event. The buffer is reset.
func (i *Input) put() (event.RoomMessageEvent, bool) {
	head := i.buffer.StartIter()
	tail := i.buffer.EndIter()

	// Get the buffer without any invisible segments, since those segments
	// contain HTML.
	plain := i.buffer.Text(&head, &tail, false)
	if plain == "" {
		return event.RoomMessageEvent{}, false
	}

	ev := event.RoomMessageEvent{
		RoomEventInfo: event.RoomEventInfo{RoomID: i.roomID},
		Body:          plain,
		MsgType:       event.RoomMessageText,
		RelatesTo:     inReplyTo(i.replyingTo),
	}

	var html strings.Builder

	// Get the buffer WITH the invisible HTML segments.
	inputHTML := i.buffer.Text(&head, &tail, true)

	if err := md.Converter.Convert([]byte(inputHTML), &html); err == nil {
		var out string
		out = html.String()
		out = strings.TrimSpace(out)
		out = strings.TrimPrefix(out, "<p>") // we don't need these tags
		out = strings.TrimSuffix(out, "</p>")
		out = strings.TrimSpace(out)

		ev.Format = event.FormatHTML
		ev.FormattedBody = out
	}

	return ev, true
}

func inReplyTo(eventID matrix.EventID) json.RawMessage {
	if eventID == "" {
		return nil
	}

	var relatesTo struct {
		InReplyTo struct {
			EventID matrix.EventID `json:"event_id"`
		} `json:"m.in_reply_to"`
	}

	relatesTo.InReplyTo.EventID = eventID

	b, err := json.Marshal(relatesTo)
	if err != nil {
		log.Panicf("error marshaling relatesTo: %v", err) // bug
	}

	return b
}

func finishAutocomplete(
	ctx context.Context,
	text *gtk.TextView,
	buffer *gtk.TextBuffer,
	row autocomplete.SelectedData) bool {

	buffer.BeginUserAction()
	defer buffer.EndUserAction()

	// Delete the inserted text, which will equalize the two bounds. The
	// caller will use bounds[1], so we use that to revalidate it.
	buffer.Delete(row.Bounds[0], row.Bounds[1])

	// TODO: use TextMarks instead, maybe?

	switch data := row.Data.(type) {
	case autocomplete.RoomMemberData:
		client := gotktrix.FromContext(ctx).Offline()

		md.InsertInvisible(row.Bounds[1], fmt.Sprintf(
			`<a href="https://matrix.to/#/%s">`,
			html.EscapeString(string(data.ID)),
		))
		mauthor.Text(
			client, row.Bounds[1], data.Room, data.ID,
			mauthor.WithWidgetColor(text), mauthor.WithMention(),
		)
		md.InsertInvisible(row.Bounds[1], "</a>")

	case autocomplete.EmojiData:
		if data.Unicode != "" {
			// Unicode emoji means we can just insert it in plain text.
			buffer.Insert(row.Bounds[1], data.Unicode, len(data.Unicode))
		} else {
			// Queue inserting the pixbuf.
			client := gotktrix.FromContext(ctx).Offline()
			url, _ := client.SquareThumbnail(data.Custom.URL, inlineEmojiSize)
			md.AsyncInsertImage(ctx, row.Bounds[1], url, imgutil.WithRectRescale(inlineEmojiSize))
			// Insert the HTML.
			md.InsertInvisible(row.Bounds[1], customEmojiHTML(data))
		}
	default:
		log.Panicf("unknown data type %T", data)
	}

	return true
}

// requestAllMembers asynchronously fills up the local state with the given
// room's members.
func requestAllMembers(ctx context.Context, roomID matrix.RoomID) {
	client := gotktrix.FromContext(ctx)

	if err := client.RoomEnsureMembers(roomID); err != nil {
		app.Error(ctx, errors.Wrap(err, "failed to prefetch members"))
	}
}
