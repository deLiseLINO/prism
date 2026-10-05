# Vision input and sidecar

Image-capable models receive image input directly. When an image request routes to a text-only target, an enabled image-capable sidecar can describe the image for that target.

## Sub-features

- Image capability comes from the model settings override when present, otherwise the catalog value.
- The Overview sidecar card requires the experimental `visionSidecar` flag. It lists eligible enabled image-capable targets.
- The sidecar description request uses the normal router and a canonical non-streaming request. There is no universal responses-only provider requirement.
- Enabling the sidecar does not mark other models image-capable in generated client configs.
- Description failure produces an unavailable-description note for downstream input. It does not invent image content.

## How to get to it

Enable the sidecar flag on Experimental, then open Overview. Choose an eligible target and enable the sidecar. For direct image input, select an image-capable model in the client.

## Verification recipe and limits

The legacy `vision-sidecar-proof.sh` has not migrated to checked process and CDP ownership. Do not count it as a safe owned-runtime proof or run it against a shared app.

A future owned drive must verify the Overview controls through visible clicks and compare the selected target against the management API. Send the same known image to an image-capable target directly and through a text-only target with the sidecar enabled. Compare the response to facts visible in that image, not merely an HTTP 200.

Image ingress shapes and adapter behavior need separate proof. A single configured upstream does not establish a universal wire restriction.

## Gotchas

- Never edit the user's state JSON to select a target. Use generation-checked management writes against an owned daemon.
- Discover an eligible target from the current catalog. Do not hard-code a user's provider, model, or endpoint into evidence.
- Native or upstream image behavior is unavailable when its prerequisite is missing. A source read alone does not prove image inference.
