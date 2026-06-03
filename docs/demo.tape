# Regenerate docs/demo.gif:
#   vhs docs/demo.tape
#
# Requires mnil built at ./mnil and `vhs` installed (brew install vhs).

Output docs/demo.gif

Set Shell "bash"
Set FontSize 13
Set Width 1100
Set Height 600
Set Theme "Dracula"
Set TypingSpeed 90ms
Set Framerate 24
Set Padding 20

Type "./mnil 'bash docs/demo-source.sh'"
Enter
Sleep 4s

# Live search for ERROR — matches highlight as we type
Type "/"
Sleep 600ms
Type "ERROR"
Sleep 2200ms

# Tab through matches
Tab
Sleep 1000ms
Tab
Sleep 1000ms
Tab
Sleep 1000ms
Escape

# Toggle line wrap (shows the WRAP chip + long lines folding)
Sleep 1s
Type "w"
Sleep 2800ms
Type "w"

# Show full help (?)
Sleep 800ms
Type "?"
Sleep 3000ms
Type "?"

# Quit
Sleep 800ms
Type "q"
Sleep 500ms
