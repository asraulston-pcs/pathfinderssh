# Credentials and the vault

The vault is one encrypted file holding the logins your sessions, crawls and
captures use. **Vault → Manage credentials** opens it.

![The vault manager](images/vault_dialog.png)

A crawl reaching a hundred devices cannot stop to ask for a password a hundred
times, and typing one into a run dialog puts it in a field that is easy to
leave filled in. The vault is how a run gets a credential without either.

## The fields

| Field | What it does |
| --- | --- |
| Name | What you call this credential. It is what a session's **Vault credential** dropdown shows. |
| Username | The login user. |
| Auth | Password, or a public key. |
| Tags | Optional labels. A run can ask for only credentials carrying particular tags. |
| Scope | Which devices this credential may be offered to. `any` is every device. |

**Make default** marks one credential as the one used when nothing more
specific applies. A run that names no credential and no tag still has something
to try, which is what makes the crawl in the Quickstart work with no
credential configuration at all.

**Disable** keeps a credential without offering it. Useful for one that has
been rotated but might need to come back.

**Master password** changes the password protecting the file.

## How a run picks one

For each device, in order:

1. A credential explicitly named on the session.
2. Credentials matching the run's **Credential tags**, in priority order.
3. The default credential.
4. Anything typed on the run dialog's Credentials tab.

Several credentials can be tried per device — a key first, a password as
fallback. If you rely on that ladder and use tags to select it, **give every
rung the same tag**. A tag filter requires a credential to carry all the tags
asked for, so two rungs with different tags have no single tag value that
selects both, and the second is silently never offered.

## What is stored, and what is not

The file holds credentials encrypted with a key derived from your master
password. The master password itself is never written anywhere — a wrong one is
detected by the decryption failing, not by comparing against a stored copy.

Passwords and key passphrases typed into a **session** are not written to the
session file at all. They exist for that connection and are gone when it
closes. This is why the session file is safe to keep in version control and the
vault is not.
