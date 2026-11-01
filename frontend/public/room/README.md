# Whale room artwork

Created with the built-in image generation tool on 2026-11-02, following the user's approved whale-companion room concept. These are AI-generated fan-character interpretations, not official DeepSeek assets or a claim of endorsement. No Live2D SDK or downloaded third-party model is included. User-provided replacements remain subject to their owners' rights.

- `whale-bedroom.png`: empty 3:2 daylight bedroom background. Prompt: retain left window/desk, right blue-quilt bed, pale oak/cream palette; remove character, tail and desk chair to allow compositing.
- `whale-poses.png`: transparent single-row four-pose sheet, ordered rest / phone / desk / walk. Prompt: consistent blue-gradient whale girl, head fins, thick whale tail, navy-white apron dress; isolated full-body poses, no room or labels. Actual sheet is 2172 x 724 (four 543 x 724 cells), rather than the requested square cells. CSS uses those actual cell proportions.

These are static pose layers animated with CSS and a cancellable state machine, not skeletal animation or frame-by-frame walk cycles. Minor edge/pose transitions remain illustration-style; this is not a physics or occlusion system. Files are served locally from the app base URL, not from a third-party CDN.
