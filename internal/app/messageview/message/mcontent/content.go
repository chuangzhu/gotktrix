package mcontent

import (
	"context"

	"github.com/chanbakjsd/gotrix/event"
	"github.com/chanbakjsd/gotrix/matrix"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotktrix/internal/gotktrix"
	"github.com/diamondburned/gotktrix/internal/gotktrix/events/m"
)

const (
	maxWidth  = 250
	maxHeight = 300
)

// Content is a message content widget.
type Content struct {
	*gtk.Box
	ev  *gotktrix.EventBox
	ctx context.Context

	part  contentPart
	react *reactionBox

	editedTime matrix.Timestamp
}

// New parses the given room message event and renders it into a Content widget.
func New(ctx context.Context, msgBox *gotktrix.EventBox) *Content {
	e, err := msgBox.Parse()
	if err != nil || e.Type() != event.TypeRoomMessage {
		return wrapParts(ctx, msgBox, newUnknownContent(ctx, msgBox))
	}

	msg, ok := e.(event.RoomMessageEvent)
	if !ok {
		return wrapParts(ctx, msgBox, newUnknownContent(ctx, msgBox))
	}

	switch msg.MsgType {
	case event.RoomMessageNotice:
		fallthrough
	case event.RoomMessageText:
		return wrapParts(ctx, msgBox, newTextContent(ctx, msgBox))
	case event.RoomMessageEmote:
		return wrapParts(ctx, msgBox, newTextContent(ctx, msgBox))
	case event.RoomMessageVideo:
		return wrapParts(ctx, msgBox, newVideoContent(ctx, msg))
	case event.RoomMessageImage:
		return wrapParts(ctx, msgBox, newImageContent(ctx, msg))

	// case event.RoomMessageEmote:
	// case event.RoomMessageFile:
	// case event.RoomMessageAudio:
	// case event.RoomMessageLocation:
	default:
		return wrapParts(ctx, msgBox, newUnknownContent(ctx, msgBox))
	}
}

func wrapParts(ctx context.Context, msgBox *gotktrix.EventBox, part contentPart) *Content {
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.SetHExpand(true)
	box.Append(part)

	reactions := newReactionBox()
	reactions.AddCSSClass("mcontent-reactionrev")
	box.Append(reactions)

	client := gotktrix.FromContext(ctx)
	runsub := client.SubscribeRoom(msgBox.RoomID, m.ReactionEventType, func(ev event.Event) {
		reaction := ev.(m.ReactionEvent)
		glib.IdleAdd(func() {
			reactions.Add(ctx, reaction)
		})
	})

	box.ConnectUnrealize(func() {
		runsub()
	})

	return &Content{
		Box:   box,
		ev:    msgBox,
		ctx:   ctx,
		part:  part,
		react: reactions,
	}
}

type extraMenuSetter interface {
	SetExtraMenu(gio.MenuModeller)
}

// SetExtraMenu sets the extra menu for the message content.
func (c *Content) SetExtraMenu(menu gio.MenuModeller) {
	s, ok := c.part.(extraMenuSetter)
	if ok {
		s.SetExtraMenu(menu)
	}
}

// EditedTimestamp returns either the Matrix timestamp if the message content
// has been edited or false if not.
func (c *Content) EditedTimestamp() (matrix.Timestamp, bool) {
	return c.editedTime, c.editedTime > 0
}

func (c *Content) OnRelatedEvent(box *gotktrix.EventBox) {
	if c.isRedacted() {
		return
	}

	ev, err := box.Parse()
	if err != nil {
		return
	}

	switch ev := ev.(type) {
	case event.RoomMessageEvent:
		if body, isEdited := MsgBody(box); isEdited {
			if editor, ok := c.part.(editableContentPart); ok {
				editor.edit(body)
				c.editedTime = ev.OriginTime
			}
		}
	case event.RoomRedactionEvent:
		if ev.Redacts == c.ev.ID {
			// Redacting this message itself.
			c.redact(ev)
			return
		}
		// TODO: if we have a proper graph data structure that keeps track of
		// relational events separately instead of keeping it nested in its
		// respective events, then we wouldn't need to do this.
		if c.react.Remove(c.ctx, ev) {
			return
		}
	case m.ReactionEvent:
		if ev.RelatesTo.RelType == "m.annotation" {
			c.react.Add(c.ctx, ev)
		}
	}
}

func (c *Content) LoadMore() {
	if l, ok := c.part.(loadableContentPart); ok {
		l.LoadMore()
	}
}

func (c *Content) isRedacted() bool {
	_, ok := c.part.(redactedContent)
	return ok
}

func (c *Content) redact(red event.RoomRedactionEvent) {
	c.Box.Remove(c.part)
	c.react.RemoveAll()

	c.part = newRedactedContent(c.ctx, red)
	c.Box.Prepend(c.part)
}
