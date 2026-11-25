package carddav

import (
	"errors"
	"net/http"
	"testing"
)

// Re-audit R22: writability was tracked per source only, and a 403 PUT
// from a read-only first address book was a plain error — the save
// failed silently and the second (writable) book was never tried.

func TestParseCurrentUserPrivilegeSet(t *testing.T) {
	writeBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/ab/</D:href><D:propstat><D:prop>
<D:current-user-privilege-set>
<D:privilege><D:read/></D:privilege>
<D:privilege><D:write-content/></D:privilege>
</D:current-user-privilege-set>
</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	writable, known := parseCurrentUserPrivilegeSet([]byte(writeBody))
	if !writable || !known {
		t.Errorf("write-content privilege: writable=%v known=%v, want true/true", writable, known)
	}

	readOnlyBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/ab/</D:href><D:propstat><D:prop>
<D:current-user-privilege-set>
<D:privilege><D:read/></D:privilege>
</D:current-user-privilege-set>
</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	writable, known = parseCurrentUserPrivilegeSet([]byte(readOnlyBody))
	if writable || !known {
		t.Errorf("read-only privilege set: writable=%v known=%v, want false/true", writable, known)
	}

	noPrivilegeBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/ab/</D:href><D:propstat><D:prop>
<D:displayname>Contacts</D:displayname>
</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	writable, known = parseCurrentUserPrivilegeSet([]byte(noPrivilegeBody))
	if writable || known {
		t.Errorf("no privilege set: writable=%v known=%v, want false/false (inconclusive)", writable, known)
	}

	writable, known = parseCurrentUserPrivilegeSet([]byte("not xml at all"))
	if writable || known {
		t.Errorf("unparsable body: writable=%v known=%v, want false/false", writable, known)
	}
}

func TestPutContact_ForbiddenIsTyped(t *testing.T) {
	fake := newFakeServer(http.StatusForbidden, "", "denied")
	defer fake.close()

	c := newTestClient(t, fake.srv.URL)
	_, err := c.PutContact("/addressbook/", "/addressbook/contact.vcf", "", true, []byte("dummy"))
	if err == nil {
		t.Fatal("expected error on 403")
	}
	var forbidden *ErrForbidden
	if !errors.As(err, &forbidden) {
		t.Fatalf("expected *ErrForbidden, got %T: %v", err, err)
	}
	if forbidden.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", forbidden.Status)
	}
	if !IsWriteForbidden(err) {
		t.Errorf("IsWriteForbidden = false for a 403 PUT error")
	}
	if IsWriteForbidden(nil) {
		t.Errorf("IsWriteForbidden(nil) = true, want false")
	}
}

func TestDeleteContact_ForbiddenIsTyped(t *testing.T) {
	fake := newFakeServer(http.StatusForbidden, "", "denied")
	defer fake.close()

	c := newTestClient(t, fake.srv.URL)
	err := c.DeleteContact("/addressbook/", "/addressbook/contact.vcf", "")
	var forbidden *ErrForbidden
	if !errors.As(err, &forbidden) {
		t.Fatalf("expected *ErrForbidden, got %T: %v", err, err)
	}
}

func TestStoreAddressbookWritable(t *testing.T) {
	db := openCardDAVTestDB(t)
	s := NewStore(db.DB)

	src, err := s.CreateSource(&SourceConfig{
		Name: "Test DAV", Type: SourceTypeCardDAV, URL: "https://example/",
		Username: "u", Enabled: true, Writable: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A book created without a probe starts writable.
	ab, err := s.CreateAddressbook(src.ID, "/ab1/", "First", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ab.Writable {
		t.Errorf("CreateAddressbook: Writable = false, want true (unprobed books are optimistic)")
	}

	// A probe that found no write privilege stores read-only, and
	// the flag round-trips through Get/List.
	ab2, err := s.CreateAddressbookWritable(src.ID, "/ab2/", "Second", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if ab2.Writable {
		t.Errorf("CreateAddressbookWritable(false): Writable = true, want false")
	}
	if err := s.SetAddressbookWritable(ab.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAddressbook(ab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Writable {
		t.Errorf("after SetAddressbookWritable(false): Writable = true, want false")
	}
	enabled, err := s.ListEnabledAddressbooks(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range enabled {
		if b.Writable {
			t.Errorf("book %q still writable after being marked read-only", b.Name)
		}
	}
	if err := s.SetAddressbookWritable("no-such-id", false); err == nil {
		t.Errorf("SetAddressbookWritable on a missing book: expected an error")
	}
}
