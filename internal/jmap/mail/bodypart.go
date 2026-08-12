package mail

import (
	"sort"
	"strconv"
	"strings"
)

// EmailBodyPart and the body-part lists — RFC 8621 §4.1.4.

// bodyPartProperties is the set §4.2's bodyProperties argument may name, and
// the default set when it is absent.
//
// §4.2 gives the default explicitly: "If omitted, this defaults to [ "partId",
// "blobId", "size", "name", "type", "charset", "disposition", "cid",
// "language", "location" ]". The full property set of §4.1.4 adds "headers",
// "subParts" and the per-header forms.
var bodyPartProperties = map[string]bool{
	"partId":      true,
	"blobId":      true,
	"size":        true,
	"name":        true,
	"type":        true,
	"charset":     true,
	"disposition": true,
	"cid":         true,
	"language":    true,
	"location":    true,
	"subParts":    true,
	"headers":     true,
}

// defaultBodyProperties is the §4.2 default list, in the RFC's own order.
var defaultBodyProperties = []string{
	"partId", "blobId", "size", "name", "type",
	"charset", "disposition", "cid", "language", "location",
}

// bodyPartTree turns the stored flat part list into the nested EmailBodyPart
// structure of §4.1.4, returning the root.
//
// The store keeps the tree flattened with a Parent index (internal/sync
// encodeStructure); JMAP wants it nested through subParts. This rebuilds the
// nesting in one pass, and is defensive about the input in a way that would be
// paranoid for data this process just produced but is correct for data read
// back from a database that other versions of this software have written: a
// part whose Parent is out of range, or which participates in a cycle, is
// attached to the root rather than dropped or followed. Mail that survives S4's
// corpus is exactly the mail that produces strange trees.
func bodyPartTree(parts []StructurePart) *bodyPartNode {
	if len(parts) == 0 {
		return nil
	}

	nodes := make([]*bodyPartNode, len(parts))
	byIndex := make(map[int]*bodyPartNode, len(parts))
	for i, p := range parts {
		nodes[i] = &bodyPartNode{part: p}
		// A duplicate Index in the stored document would otherwise make the
		// last one win silently; first wins, and the duplicate becomes a
		// root-level orphan below.
		if _, dup := byIndex[p.Index]; !dup {
			byIndex[p.Index] = nodes[i]
		}
	}

	// Choose the root FIRST, before any edge is built.
	//
	// This ordering is what makes a cyclic document safe. Attaching edges
	// first and repairing afterwards leaves a cycle in place — 0's parent is 1
	// and 1's parent is 0 — and any later walk of that structure recurses
	// forever. Picking a root and then attaching each remaining node only if
	// it is still reachable from that root builds a structure that is a TREE
	// by construction, so no walker downstream needs a depth guard to be
	// correct.
	var root *bodyPartNode
	for i, p := range parts {
		if p.Parent < 0 {
			root = nodes[i]
			break
		}
	}
	if root == nil {
		// Every part claims a parent (a cycle, or a document whose root was
		// lost). The lowest-indexed part becomes the root: some node has to
		// be, and the choice must be deterministic.
		root = nodes[0]
	}

	// attached tracks which nodes are already in the tree; a node is attached
	// only under a node that is itself attached, which is precisely the
	// invariant that forbids a cycle from forming.
	attached := map[*bodyPartNode]bool{root: true}

	// Repeat until no further progress: a child may appear before its parent
	// in the stored document, so one pass is not enough. Each pass attaches at
	// least one node or the loop stops, so this is O(n²) worst case on a
	// pathological document and linear on a real one — with n bounded by the
	// parser's MaxParts.
	for progress := true; progress; {
		progress = false
		for i, p := range parts {
			n := nodes[i]
			if attached[n] {
				continue
			}
			parent, ok := byIndex[p.Parent]
			if ok && attached[parent] && parent != n {
				parent.children = append(parent.children, n)
				attached[n] = true
				progress = true
			}
		}
	}

	// Whatever is still unattached is part of a cycle or points at a parent
	// that does not exist. It goes directly under the root rather than being
	// dropped: a part the client cannot see is a part of somebody's mail that
	// silently vanished.
	for i := range parts {
		n := nodes[i]
		if !attached[n] {
			root.children = append(root.children, n)
			attached[n] = true
		}
	}

	// Document order within each container, which is the order a MIME reader
	// would see and the order textBody/htmlBody selection depends on. The
	// structure is a tree by construction above, so this walk terminates.
	sortChildren(root)
	return root
}

// sortChildren orders each container's children by their stored index.
func sortChildren(n *bodyPartNode) {
	sort.SliceStable(n.children, func(i, j int) bool {
		return n.children[i].part.Index < n.children[j].part.Index
	})
	for _, c := range n.children {
		sortChildren(c)
	}
}

// bodyPartNode is one node of the reconstructed tree.
type bodyPartNode struct {
	part     StructurePart
	children []*bodyPartNode
}

// isMultipart reports whether the node is a container.
func (n *bodyPartNode) isMultipart() bool {
	return n.part.IsMultipart || strings.HasPrefix(strings.ToLower(n.part.MediaType), "multipart/")
}

// mediaType returns the lowercased type/subtype, defaulting per RFC 2045 to
// text/plain when the stored value is empty.
func (n *bodyPartNode) mediaType() string {
	mt := strings.ToLower(strings.TrimSpace(n.part.MediaType))
	if mt == "" {
		return "text/plain"
	}
	return mt
}

// isInlineText reports whether this leaf is body text as §4.1.4's selection
// algorithm means it: a text/* part not marked as an attachment.
func (n *bodyPartNode) isInlineText() bool {
	return strings.HasPrefix(n.mediaType(), "text/") && !n.isAttachmentPart()
}

// isAttachmentPart reports whether the part presents as an attachment.
//
// The parser already made this decision at the parse layer (S4 corpus
// convention C2) and the store persisted it, so this trusts that flag and only
// adds the Content-Disposition check for rows written before the flag existed.
func (n *bodyPartNode) isAttachmentPart() bool {
	return n.part.IsAttachment || strings.EqualFold(n.part.Disposition, "attachment")
}

// bodyStructureLists computes textBody, htmlBody and attachments from the
// tree, per the algorithm of RFC 8621 §4.1.4.
//
// The RFC describes it as a recursive walk that, for a multipart/alternative,
// picks the text alternative for textBody and the HTML alternative for
// htmlBody, and treats everything else that is not inline body content as an
// attachment. The implementation below follows that description directly:
//
//   - multipart/alternative: the FIRST text/plain-ish child feeds textBody and
//     the FIRST text/html-ish child feeds htmlBody, each recursively; any other
//     child's attachments are collected. (§4.1.4: "in the case of
//     multipart/alternative, the algorithm picks one branch for textBody and
//     another for htmlBody" — the two lists deliberately disagree here, which
//     is the entire point of the property pair.)
//   - multipart/related: the root part is the body; the other parts are
//     attachments referenced by cid.
//   - any other multipart: every child contributes to both lists in order.
//   - a leaf: inline text goes to the body lists, everything else to
//     attachments.
func bodyStructureLists(root *bodyPartNode) (textBody, htmlBody, attachments []*bodyPartNode) {
	if root == nil {
		return nil, nil, nil
	}
	// A leaf contributes itself: inline text to both body lists, anything else
	// to attachments.
	if !root.isMultipart() {
		if root.isInlineText() {
			return []*bodyPartNode{root}, []*bodyPartNode{root}, nil
		}
		return nil, nil, []*bodyPartNode{root}
	}

	// A container dispatches on its own media type. Recursion goes through
	// bodyStructureLists itself, which is what lets multipart/alternative ask
	// for only the text half of one branch and only the HTML half of another.
	{
		n := root
		switch n.mediaType() {
		case "multipart/alternative":
			var textPick, htmlPick *bodyPartNode
			for _, c := range n.children {
				if textPick == nil && alternativeIsText(c) {
					textPick = c
					continue
				}
				if htmlPick == nil && alternativeIsHTML(c) {
					htmlPick = c
					continue
				}
			}
			// A single-branch alternative (only text, or only html) must still
			// produce a body in BOTH lists: a client that asked for htmlBody
			// on a text-only message must get the text rather than nothing,
			// which is what makes htmlBody usable as "the body to render".
			//
			// The fallback is assigned before the loop below, because that
			// loop dispatches on identity against these two variables.
			if textPick == nil {
				textPick = htmlPick
			}
			if htmlPick == nil {
				htmlPick = textPick
			}
			// When the two picks are the same node — a single-branch
			// alternative — that one branch must feed both lists. The switch
			// below can only match one case per child, so the shared case is
			// handled here and the loop skips it.
			if textPick != nil && textPick == htmlPick {
				t, h, att := bodyStructureLists(textPick)
				textBody = append(textBody, t...)
				htmlBody = append(htmlBody, h...)
				attachments = append(attachments, att...)
			}
			for _, c := range n.children {
				if c == textPick && textPick == htmlPick {
					continue // already contributed to both lists above
				}
				switch c {
				case textPick:
					sub, _, att := bodyStructureLists(c)
					textBody = append(textBody, sub...)
					attachments = append(attachments, att...)
				case htmlPick:
					_, sub, att := bodyStructureLists(c)
					htmlBody = append(htmlBody, sub...)
					attachments = append(attachments, att...)
				default:
					// A branch chosen for neither list contributes only its
					// attachments; its text is a redundant alternative.
					_, _, att := bodyStructureLists(c)
					attachments = append(attachments, att...)
				}
			}
		case "multipart/related":
			// §4.1.4 treats the related root as the body and the rest as
			// referenced attachments. The root is the first child unless a
			// "start" parameter names another — a parameter the store does not
			// currently persist, so the first child is used and the case is
			// noted for the store gap list.
			for i, c := range n.children {
				if i == 0 {
					t, h, att := bodyStructureLists(c)
					textBody = append(textBody, t...)
					htmlBody = append(htmlBody, h...)
					attachments = append(attachments, att...)
					continue
				}
				attachments = append(attachments, collectAll(c)...)
			}

		default:
			for _, c := range n.children {
				t, h, att := bodyStructureLists(c)
				textBody = append(textBody, t...)
				htmlBody = append(htmlBody, h...)
				attachments = append(attachments, att...)
			}
		}
	}
	return textBody, htmlBody, attachments
}

// alternativeIsText reports whether a multipart/alternative branch is the one
// textBody should follow.
func alternativeIsText(n *bodyPartNode) bool {
	if n.isMultipart() {
		// A nested multipart inside an alternative is a body branch (commonly
		// multipart/related wrapping the HTML). It counts as the text branch
		// only if it is not the html one.
		return !containsHTML(n)
	}
	return n.mediaType() == "text/plain" && !n.isAttachmentPart()
}

// alternativeIsHTML reports whether a branch is the one htmlBody should follow.
func alternativeIsHTML(n *bodyPartNode) bool {
	if n.isMultipart() {
		return containsHTML(n)
	}
	return n.mediaType() == "text/html" && !n.isAttachmentPart()
}

// containsHTML reports whether a subtree holds an inline text/html part.
func containsHTML(n *bodyPartNode) bool {
	if n == nil {
		return false
	}
	if !n.isMultipart() {
		return n.mediaType() == "text/html" && !n.isAttachmentPart()
	}
	for _, c := range n.children {
		if containsHTML(c) {
			return true
		}
	}
	return false
}

// collectAll returns every leaf of a subtree, for the branches that are
// attachments wholesale.
func collectAll(n *bodyPartNode) []*bodyPartNode {
	if n == nil {
		return nil
	}
	if !n.isMultipart() {
		return []*bodyPartNode{n}
	}
	var out []*bodyPartNode
	for _, c := range n.children {
		out = append(out, collectAll(c)...)
	}
	return out
}

// partID is the §4.1.4 partId: "This is a server-defined identifier for the
// part... It is unique within the Email."
//
// The stored part index serves directly: it is unique within the message and
// stable for as long as the parse is, which is what a client caching a partId
// needs. It is rendered as a decimal string because partId is typed String.
func partID(p StructurePart) string { return strconv.Itoa(p.Index) }

// partBlobID is the §4.1.4 blobId of a body part: "The id representing the raw
// octets of the contents of the part... This may be used to download the raw
// contents".
//
// Phase 1 stores blobs per MESSAGE, not per part: internal/sync's
// encodeStructure deliberately keeps part content out of the database, and the
// blob store holds the whole raw message. There is therefore no per-part blob
// to name, and inventing an id that download cannot serve would be worse than
// null — a client would offer a download that 404s.
//
// So part blobIds are null in phase 1, the message's own blobId serves the
// whole raw message, and per-part blobs are recorded in the J2 report as a
// store gap for the epic that adds a part-addressed blob (blob_refs already
// has the 'part' owner_kind reserved for exactly this).
func partBlobID() any { return nil }
