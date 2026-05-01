# Agent App Icons

Avatar icons for agents in the agentic SDLC system.

## Generation

These icons were generated using Google Gemini image generation with the following prompt:

> Design a set of avatar icons for agents in an agentic SDLC system. They are "bootstrap", "triage", "coder", "review", "prioritize", "refinement", "discovery", "user research", "tech research", "upstream", "scribe", and "retro". The icons should be easy to distinguish when small. Do it with a flat, material design. Do it with a hip, off-beat color pallette.

## Extraction

The generation produced a single composite image with all icons in a grid. Individual icons were extracted using Claude Code with the following prompt:

> Take this image and isolate all the individual icons and save them as separate pngs

Extraction used OpenCV Hough circle detection (`cv2.HoughCircles`) to locate the exact center and radius of each circular icon in the composite image, then cropped each one with Pillow.
