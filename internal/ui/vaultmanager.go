// internal/ui/vaultmanager.go
//
// The credential manager window: the list, the editor, and the master
// password.
//
// Everything the manager DECIDES is in vaultmodel.go, which has no toolkit in
// it and is tested. This file is layout, callbacks and the confirmations that
// stand in front of the destructive actions.
//
// # Why this exists when pfvault already does all of it
//
// It does, and that is the point: the CLI has had add/list/rm/disable/enable/
// default since the beginning, and an estate's credentials still get set up
// through it once and then never looked at. The fields that decide which
// credential a 670-device crawl tries FIRST -- priority, scope, tags -- are
// the ones nobody tunes from a shell, and the ones whose effect is invisible
// until a run comes back with a column of authentication failures.
//
// # What it deliberately does not do
//
// It never shows a stored secret. The list is built from vault.Meta, which is
// redacted by construction, and the editor is built from the same Meta rather
// than from a decrypted Credential -- see vaultmodel.go. Changing the master
// password is here because internal/vault has implemented it since the
// beginning and nothing has ever called it, so today it cannot be changed at
// all.
package ui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/vault"
)

// VaultActions is what the manager needs from the application.
//
// An interface rather than a *vault.Vault so this package does not decide how
// a vault is opened, locked or shared between windows -- the host owns one
// vault per app session and that arrangement is what keeps a GUI from falling
// through to a terminal password prompt it can never answer.
type VaultActions interface {
	// List returns the redacted credential list.
	List() ([]vault.Meta, error)

	// Get returns a full credential, secrets included. The manager calls
	// it only to carry material forward across an edit, never to display.
	Get(idOrName string) (vault.Credential, error)

	Add(c vault.Credential) (vault.Credential, error)
	Update(c vault.Credential) error
	Delete(id string) error

	SetDefault(id string) error
	SetDisabled(id string, disabled bool) error

	ChangeMasterPassword(oldMaster, newMaster string) error

	// Path is shown in the title so somebody with a legacy vault and a
	// current one can tell which is open.
	Path() string
}

// DefaultClearer is implemented by a vault that can unset the default.
//
// Optional, and deliberately a separate interface: internal/vault has
// SetDefault but no ClearDefault in every tree this has been built against, so
// requiring it here would make the manager refuse to compile against the vault
// it manages. When it is absent the button says why instead of doing nothing.
type DefaultClearer interface {
	ClearDefault() error
}

// ShowVaultManager opens the credential manager over w.
//
// onChanged is called after any change that lands, so the host can rebuild the
// credential list its dialogs offer. Passing nil is legal and means nothing
// else needs telling.
func ShowVaultManager(w fyne.Window, v VaultActions, onChanged func()) {
	if v == nil {
		dialog.ShowInformation("Vault", "No vault is unlocked.", w)
		return
	}

	m := &vaultManager{win: w, vault: v, onChanged: onChanged}
	m.build()
	m.reload()
	m.show()
}

type vaultManager struct {
	win       fyne.Window
	vault     VaultActions
	onChanged func()

	rows  []VaultRow
	metas []vault.Meta

	list   *widget.List
	status *widget.Label
	dlg    dialog.Dialog

	// selected is an index into rows, or -1. Held rather than read from
	// the widget because a List reports selection through a callback and
	// there is no "what is selected" question to ask it.
	selected int

	editBtn, deleteBtn, defaultBtn, disableBtn *widget.Button
}

func (m *vaultManager) build() {
	m.selected = -1
	m.status = statusLabel()

	m.list = widget.NewList(
		func() int { return len(m.rows) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel("name"),
				widget.NewLabel("user"),
				widget.NewLabel("auth"),
				widget.NewLabel("scope"),
				widget.NewLabel("flags"),
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < 0 || i >= len(m.rows) {
				return
			}
			r := m.rows[i]
			cells := o.(*fyne.Container).Objects
			set := func(n int, s string) {
				lb := cells[n].(*widget.Label)
				lb.SetText(s)
				// A disabled credential is out of automatic
				// selection; graying it says so without the
				// operator having to read the flags column.
				lb.Importance = widget.MediumImportance
				if r.Disabled {
					lb.Importance = widget.LowImportance
				}
				lb.Refresh()
			}
			set(0, r.Name)
			set(1, r.Username)
			set(2, r.Auth)
			set(3, r.Scope)
			set(4, r.Flags)
		},
	)
	m.list.OnSelected = func(i widget.ListItemID) {
		m.selected = int(i)
		m.updateButtons()
	}
	m.list.OnUnselected = func(i widget.ListItemID) {
		if m.selected == int(i) {
			m.selected = -1
			m.updateButtons()
		}
	}

	m.editBtn = widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), m.editSelected)
	m.deleteBtn = widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), m.deleteSelected)
	m.defaultBtn = widget.NewButtonWithIcon("Make default", theme.ConfirmIcon(), m.defaultSelected)
	m.disableBtn = widget.NewButtonWithIcon("Disable", theme.VisibilityOffIcon(), m.toggleSelected)
	m.updateButtons()
}

func (m *vaultManager) show() {
	actions := container.NewHBox(
		widget.NewButtonWithIcon("New", theme.ContentAddIcon(), m.addNew),
		m.editBtn, m.deleteBtn, m.defaultBtn, m.disableBtn,
		layoutSpacer(),
		widget.NewButtonWithIcon("Master password", theme.SettingsIcon(), m.changeMaster),
	)
	content := container.NewBorder(actions, m.status, nil, nil, m.list)

	m.dlg = dialog.NewCustom("Vault — "+m.vault.Path(), "Close", content, m.win)
	m.dlg.Resize(fyne.NewSize(860, 520))
	m.dlg.Show()
}

func layoutSpacer() fyne.CanvasObject { return widget.NewLabel("   ") }

// reload rebuilds the list from the vault. Called after every change rather
// than patching a row, so what is on screen can never be a version of the
// vault that was never written.
func (m *vaultManager) reload() {
	metas, err := m.vault.List()
	if err != nil {
		m.status.SetText("⚠  " + err.Error())
		return
	}
	m.metas = metas
	m.rows = VaultRows(metas)
	m.selected = -1
	if m.list != nil {
		m.list.UnselectAll()
		m.list.Refresh()
	}
	m.updateButtons()
	m.status.SetText(fmt.Sprintf("%d credential(s)", len(m.rows)))
}

// metaFor maps the selected ROW back to a vault record. Rows are sorted for
// display, so an index into rows is not an index into metas.
func (m *vaultManager) metaFor(i int) (vault.Meta, bool) {
	if i < 0 || i >= len(m.rows) {
		return vault.Meta{}, false
	}
	id := m.rows[i].ID
	for _, meta := range m.metas {
		if meta.ID == id {
			return meta, true
		}
	}
	return vault.Meta{}, false
}

func (m *vaultManager) updateButtons() {
	sel, ok := m.metaFor(m.selected)
	for _, b := range []*widget.Button{m.editBtn, m.deleteBtn, m.defaultBtn, m.disableBtn} {
		if b == nil {
			continue
		}
		// Greyed rather than hidden: a button that disappears takes its
		// explanation with it.
		if ok {
			b.Enable()
		} else {
			b.Disable()
		}
	}
	if m.disableBtn != nil {
		if ok && sel.Disabled {
			m.disableBtn.SetText("Enable")
			m.disableBtn.SetIcon(theme.VisibilityIcon())
		} else {
			m.disableBtn.SetText("Disable")
			m.disableBtn.SetIcon(theme.VisibilityOffIcon())
		}
	}
	if m.defaultBtn != nil && ok && sel.IsDefault {
		m.defaultBtn.SetText("Clear default")
	} else if m.defaultBtn != nil {
		m.defaultBtn.SetText("Make default")
	}
}

func (m *vaultManager) changed(what string) {
	m.reload()
	m.status.SetText(what)
	if m.onChanged != nil {
		m.onChanged()
	}
}

func (m *vaultManager) fail(err error) {
	m.status.SetText("⚠  " + err.Error())
}

// ---- actions ----

func (m *vaultManager) addNew() {
	m.showForm("New credential", NewCredentialForm(), "", func(f CredentialForm) error {
		_, err := m.vault.Add(f.Credential(vault.Credential{}))
		return err
	})
}

func (m *vaultManager) editSelected() {
	meta, ok := m.metaFor(m.selected)
	if !ok {
		return
	}
	m.showForm("Edit "+meta.Name, FormFor(meta), meta.ID, func(f CredentialForm) error {
		// The stored credential is fetched here and NOT to display: it
		// carries the secret the form deliberately never held, so a
		// blank password field keeps what is already there.
		base, err := m.vault.Get(meta.ID)
		if err != nil {
			return err
		}
		return m.vault.Update(f.Credential(base))
	})
}

func (m *vaultManager) deleteSelected() {
	meta, ok := m.metaFor(m.selected)
	if !ok {
		return
	}
	msg := fmt.Sprintf("Delete %q?\n\nThe secret it holds cannot be recovered.", meta.Name)
	if meta.IsDefault {
		msg += "\n\nIt is the default credential: sessions that name none will have nothing to use."
	}
	dialog.ShowConfirm("Delete credential", msg, func(ok bool) {
		if !ok {
			return
		}
		if err := m.vault.Delete(meta.ID); err != nil {
			m.fail(err)
			return
		}
		m.changed("deleted " + meta.Name)
	}, m.win)
}

func (m *vaultManager) defaultSelected() {
	meta, ok := m.metaFor(m.selected)
	if !ok {
		return
	}
	if meta.IsDefault {
		clearer, ok := m.vault.(DefaultClearer)
		if !ok {
			m.status.SetText("⚠  this vault cannot unset a default; make another credential the default instead")
			return
		}
		if err := clearer.ClearDefault(); err != nil {
			m.fail(err)
			return
		}
		m.changed("no default credential")
		return
	}
	// The vault refuses a disabled default, and saying so beats letting
	// the error come back from underneath.
	if meta.Disabled {
		m.status.SetText("⚠  " + meta.Name + " is disabled; enable it before making it the default")
		return
	}
	if err := m.vault.SetDefault(meta.ID); err != nil {
		m.fail(err)
		return
	}
	m.changed(meta.Name + " is now the default")
}

func (m *vaultManager) toggleSelected() {
	meta, ok := m.metaFor(m.selected)
	if !ok {
		return
	}
	if err := m.vault.SetDisabled(meta.ID, !meta.Disabled); err != nil {
		m.fail(err)
		return
	}
	if meta.Disabled {
		m.changed(meta.Name + " enabled")
		return
	}
	m.changed(meta.Name + " disabled — it stays fetchable by name, but nothing will pick it automatically")
}

// ---- the editor ----

func (m *vaultManager) showForm(title string, f CredentialForm, id string, save func(CredentialForm) error) {
	name := entryWith(f.Name)
	user := entryWith(f.Username)

	auth := widget.NewSelect(AuthChoices, nil)
	auth.SetSelected(f.AuthType)

	pass := widget.NewPasswordEntry()
	if f.HasSecret {
		// The one place the no-secret-in-the-form rule has to be
		// explained, because a blank field on an existing credential
		// otherwise reads as "there isn't one".
		pass.SetPlaceHolder("unchanged — type to replace")
	}
	keyPath := entryWith(f.KeyPath)
	passphrase := widget.NewPasswordEntry()

	desc := entryWith(f.Description)
	priority := entryWith(f.Priority)
	priority.SetPlaceHolder("0 — lower runs first")
	tags := entryWith(f.Tags)
	tags.SetPlaceHolder("comma separated")

	domain := entryWith(f.DomainSuffix)
	domain.SetPlaceHolder("lab.example — blank matches every device")
	cidrs := entryWith(f.CIDRs)
	cidrs.SetPlaceHolder("10.0.0.0/8, 192.168.0.0/16")
	platforms := entryWith(f.Platforms)
	platforms.SetPlaceHolder("arista_eos, cisco_ios")

	disabled := widget.NewCheck("Disabled — keep it but never pick it automatically", nil)
	disabled.SetChecked(f.Disabled)

	status := statusLabel()

	identity := formOf(
		"Name", name,
		"Username", user,
		"Auth type", auth,
		"Password", pass,
		"Key file", keyPath,
		"Key passphrase", passphrase,
		"Description", desc,
		"", disabled,
	)
	// Scope and priority are on their own tab because they are the half
	// that decides WHICH credential a run reaches for, and mixing them
	// into the identity fields is how they end up looking optional.
	selection := formOf(
		"Priority", priority,
		"Tags", tags,
		"Domain suffix", domain,
		"Address ranges", cidrs,
		"Platforms", platforms,
	)
	tabs := container.NewAppTabs(
		container.NewTabItem("Credential", identity),
		container.NewTabItem("When it is used", selection),
	)
	content := container.NewBorder(nil, status, nil, nil, tabs)

	var show func()
	show = func() {
		d := dialog.NewCustomConfirm(title, "Save", "Cancel", content, func(ok bool) {
			if !ok {
				return
			}
			out := CredentialForm{
				Name:         name.Text,
				Username:     user.Text,
				AuthType:     auth.Selected,
				Password:     pass.Text,
				KeyPath:      ExpandHome(keyPath.Text),
				Passphrase:   passphrase.Text,
				Description:  desc.Text,
				Priority:     priority.Text,
				Tags:         tags.Text,
				DomainSuffix: domain.Text,
				CIDRs:        cidrs.Text,
				Platforms:    platforms.Text,
				Disabled:     disabled.Checked,
				HasSecret:    f.HasSecret,
			}
			if errs := out.Validate(OtherNames(m.metas, id)); len(errs) > 0 {
				status.SetText("⚠  " + ProblemText(errs))
				show()
				return
			}
			if err := save(out); err != nil {
				status.SetText("⚠  " + err.Error())
				show()
				return
			}
			status.SetText("")
			m.changed("saved " + strings.TrimSpace(out.Name))
		}, m.win)
		d.Resize(fyne.NewSize(640, 470))
		d.Show()
	}
	show()
}

// ---- master password ----

func (m *vaultManager) changeMaster() {
	current := widget.NewPasswordEntry()
	next := widget.NewPasswordEntry()
	again := widget.NewPasswordEntry()

	items := []*widget.FormItem{
		widget.NewFormItem("Current", current),
		widget.NewFormItem("New", next),
		widget.NewFormItem("Repeat", again),
	}

	var show func()
	show = func() {
		d := dialog.NewForm("Change master password", "Change", "Cancel", items, func(ok bool) {
			if !ok {
				return
			}
			switch {
			case next.Text == "":
				m.status.SetText("⚠  the new password cannot be empty")
				show()
				return
			case next.Text != again.Text:
				// Asked twice because there is no recovery: a
				// mistyped new master password locks every
				// credential in the file permanently.
				m.status.SetText("⚠  the two new passwords do not match")
				show()
				return
			}
			if err := m.vault.ChangeMasterPassword(current.Text, next.Text); err != nil {
				m.status.SetText("⚠  " + err.Error())
				show()
				return
			}
			m.status.SetText("master password changed — a keyring entry for this vault is now stale")
		}, m.win)
		d.Resize(fyne.NewSize(460, 240))
		d.Show()
	}
	show()
}
