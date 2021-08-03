package room

import (
	"context"
	"fmt"

	"github.com/chanbakjsd/gotrix/event"
	"github.com/chanbakjsd/gotrix/matrix"
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/diamondburned/gotktrix/internal/config/prefs"
	"github.com/diamondburned/gotktrix/internal/gotktrix"
	"github.com/diamondburned/gotktrix/internal/gtkutil"
	"github.com/diamondburned/gotktrix/internal/gtkutil/cssutil"
	"github.com/diamondburned/gotktrix/internal/gtkutil/imgutil"
)

// AvatarSize is the size in pixels of the avatar.
const AvatarSize = 32

// ShowMessagePreview determines if a room shows a preview of its latest
// message.
var ShowMessagePreview = prefs.NewBool(true, prefs.PropMeta{
	Name:        "Preview Message",
	Description: "Whether or not to show a preview of the latest message.",
})

// Room is a single room row.
type Room struct {
	*gtk.ListBoxRow
	box *gtk.Box

	name    *gtk.Label
	preview *gtk.Label
	avatar  *adw.Avatar

	section Section

	ID   matrix.RoomID
	Name string
}

var avatarCSS = cssutil.Applier("roomlist-avatar", `
	.roomlist-avatar {}
`)

var roomBoxCSS = cssutil.Applier("roomlist-roombox", `
	.roomlist-roombox {
		padding: 2px 6px;
	}
	.roomlist-roomright {
		margin-left: 6px;
	}
	.roomlist-roompreview {
		font-size: 0.8em;
		color: alpha(@theme_fg_color, 0.9);
	}
`)

type Section interface {
	Client() *gotktrix.Client

	Reminify()
	Remove(*Room)
	Insert(*Room)

	OpenRoom(matrix.RoomID)
	OpenRoomInTab(matrix.RoomID)
}

// AddTo adds an empty room with the given ID to the given section..
func AddTo(section Section, roomID matrix.RoomID) *Room {
	nameLabel := gtk.NewLabel(string(roomID))
	nameLabel.SetSingleLineMode(true)
	nameLabel.SetXAlign(0)
	nameLabel.SetHExpand(true)
	nameLabel.SetEllipsize(pango.EllipsizeEnd)
	nameLabel.AddCSSClass("roomlist-roomname")

	previewLabel := gtk.NewLabel("")
	previewLabel.SetSingleLineMode(true)
	previewLabel.SetXAlign(0)
	previewLabel.SetHExpand(true)
	previewLabel.SetEllipsize(pango.EllipsizeEnd)
	previewLabel.Hide()
	previewLabel.AddCSSClass("roomlist-roompreview")

	rightBox := gtk.NewBox(gtk.OrientationVertical, 0)
	rightBox.SetVAlign(gtk.AlignCenter)
	rightBox.Append(nameLabel)
	rightBox.Append(previewLabel)
	rightBox.AddCSSClass("roomlist-roomright")

	adwAvatar := adw.NewAvatar(AvatarSize, string(roomID), false)
	avatarCSS(&adwAvatar.Widget)

	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.Append(&adwAvatar.Widget)
	box.Append(rightBox)
	roomBoxCSS(box)

	row := gtk.NewListBoxRow()
	row.SetChild(box)
	row.SetName(string(roomID))

	gtkutil.BindActionMap(row, "room", map[string]func(){
		"open":        func() { section.OpenRoom(roomID) },
		"open-in-tab": func() { section.OpenRoomInTab(roomID) },
	})

	gtkutil.BindPopoverMenu(row, [][2]string{
		{"Open", "room.open"},
		{"Open in New Tab", "room.open-in-tab"},
	})

	r := Room{
		ListBoxRow: row,
		box:        box,
		name:       nameLabel,
		preview:    previewLabel,
		avatar:     adwAvatar,

		section: section,

		ID:   roomID,
		Name: string(roomID),
	}

	section.Insert(&r)

	ShowMessagePreview.Connect(r, func() {
		r.InvalidatePreview()
	})

	return &r
}

// IsIn returns true if the room is in the given section.
func (r *Room) IsIn(s Section) bool {
	return r.section == s
}

// Move moves the room to the given section.
func (r *Room) Move(dst Section) {
	r.section.Remove(r)
	r.section = dst
	r.section.Insert(r)
}

// Changed marks the row as changed, invalidating its sorting and filter.
func (r *Room) Changed() {
	r.ListBoxRow.Changed()
	r.section.Reminify()
}

func (r *Room) SetLabel(text string) {
	r.Name = text
	r.name.SetLabel(text)
	r.avatar.SetName(text)
}

// SetAvatar sets the room's avatar URL.
func (r *Room) SetAvatarURL(mxc matrix.URL) {
	client := r.section.Client().Offline()
	url, _ := client.SquareThumbnail(mxc, AvatarSize)
	imgutil.AsyncGET(context.TODO(), url, r.avatar.SetCustomImage)
}

func (r *Room) erasePreview() {
	r.preview.SetLabel("")
	r.preview.Hide()
}

// InvalidatePreview invalidate the room's preview.
func (r *Room) InvalidatePreview() {
	if !ShowMessagePreview.Value() {
		r.erasePreview()
		return
	}

	client := r.section.Client().Offline()

	events, err := client.RoomTimeline(r.ID)
	if err != nil || len(events) == 0 {
		r.erasePreview()
		return
	}

	preview := generatePreview(client, r.ID, events[len(events)-1])
	r.preview.SetLabel(preview)
	r.preview.Show()
}

func generatePreview(c *gotktrix.Client, rID matrix.RoomID, ev event.RoomEvent) string {
	name, _ := c.MemberName(rID, ev.Sender())

	switch ev := ev.(type) {
	case event.RoomMessageEvent:
		return fmt.Sprintf("%s: %s", name.Name, trimString(ev.Body, 256))
	default:
		return fmt.Sprintf("%s: %s", name.Name, ev.Type())
	}
}

func trimString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}