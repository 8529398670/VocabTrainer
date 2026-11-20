#!/bin/bash
# Commits everything as the next number, tags that commit v1.0.<number>, and
# pushes master and the tags -- repairing any tag on GitHub that is wrong.
#
#   ./gitPushUpdate.sh            commit as the next number
#   ./gitPushUpdate.sh 42         commit as 42 (refused if v1.0.42 exists)
#   ./gitPushUpdate.sh "message"  commit with that message, tagged with the
#                                 next number; the count carries on after it
#
# The next number is one past the higher of the newest numbered commit and the
# highest v1.0.N tag on GitHub, so a worded commit never restarts the count and
# a new commit never takes a number GitHub already has. Nothing to commit
# means no new commit: it still pushes master and repairs the tags.
#
# Repair, on every run: each commit on master whose message is a number N
# should carry the tag v1.0.N.
#   - a numbered commit with no tag on GitHub gets one;
#   - a tag on a commit with a different number, or on a commit that is no
#     longer on master, is moved to the newest commit numbered N.
# A tag already on a commit with its own number is left alone, so a history
# where the count once restarted keeps its old tags. A tag whose number no
# commit has is left alone too.

Account="8529398670"
Email="banjo.kazooie.2955@gmail.com"
Remote="git@github-8529398670:8529398670/VocabTrainer.git"
# The first of these that exists is loaded into the ssh agent.
Keys=(
	"$HOME/Dropbox/Misc/SSH2/KEYS/github-8529398670"
	"$HOME/.ssh/github-8529398670"
	"/home/sd/.ssh/github-8529398670"
)

cd "$( dirname "$0" )" || exit 1

is_int() { [[ "$1" =~ ^[0-9]+$ ]]; }

# --- identity and remote ------------------------------------------------------
[ -d .git ] || git init -q -b master
git config --global --unset user.name
git config --global --unset user.email
git config user.name "$Account"
git config user.email "$Email"

ssh-add -D > /dev/null 2>&1
for key in "${Keys[@]}"; do
	if [ -f "$key" ]; then
		ssh-add -k "$key" > /dev/null 2>&1
		break
	fi
done

if git remote get-url origin > /dev/null 2>&1; then
	git remote set-url origin "$Remote"
else
	git remote add origin "$Remote"
fi

# --- what GitHub has ----------------------------------------------------------
# Asked before anything is committed: if GitHub cannot be reached, stop with
# nothing changed rather than guess a number. Without --refs, an annotated tag
# also lists the commit it points at, as a ^{} line.
if ! RemoteTags=$( git ls-remote --tags origin 'v1.0.*' ); then
	echo "Could not reach GitHub to read its tags. Nothing was committed." >&2
	exit 1
fi

# --- the next number ----------------------------------------------------------
Highest=0
while read -r _ ref; do
	number="${ref#refs/tags/v1.0.}"
	number="${number%^\{\}}"
	if is_int "$number" && (( 10#$number > Highest )); then Highest=$(( 10#$number )); fi
done <<< "$RemoteTags"

LastNumbered=0
while read -r subject; do
	if is_int "$subject"; then LastNumbered=$(( 10#$subject )); break; fi
done < <( git log master --format=%s 2> /dev/null )

if (( LastNumbered > Highest )); then Highest=$LastNumbered; fi
Next=$(( Highest + 1 ))

Message="$Next"
if [ -n "${1:-}" ]; then
	if is_int "$1"; then
		Next=$(( 10#$1 ))
		Message="$Next"
		if grep -q "refs/tags/v1.0.${Next}\$" <<< "$RemoteTags"; then
			echo "v1.0.${Next} is already on GitHub. Nothing was committed." >&2
			exit 1
		fi
	else
		Message="$1"
	fi
fi

# --- commit -------------------------------------------------------------------
Committed=false
git add -A
if git diff --cached --quiet; then
	echo "Nothing new to commit."
else
	git commit -q -m "$Message" || exit 1
	Committed=true
	echo "Committed $( git log -1 --format='%h' ) \"$Message\""
fi

git push -q origin master || exit 1

# --- tags: the new one, plus repairs ------------------------------------------
# One line per tag to push: "<create|move> <commit> <number>". Worked out in
# awk because macOS's bash 3.2 has no associative arrays.
TagPlan=$( awk '
	# master, newest first: "<commit> <subject>"
	FILENAME == ARGV[ 1 ] {
		sha = $1; subject = substr( $0 , length( $1 ) + 2 )
		if ( subject ~ /^[0-9]+$/ ) {
			n = subject + 0
			if ( ! ( n in newest ) ) newest[ n ] = sha
			numbered[ n , sha ] = 1
		}
		next
	}
	# GitHub: "<object> refs/tags/v1.0.N" and, for annotated tags,
	# "<commit> refs/tags/v1.0.N^{}" -- the commit is what counts.
	{
		ref = $2; peeled = sub( /\^\{\}$/ , "" , ref )
		sub( /^refs\/tags\/v1\.0\./ , "" , ref )
		if ( ref !~ /^[0-9]+$/ ) next
		n = ref + 0
		if ( peeled || ! ( n in remote ) ) remote[ n ] = $1
	}
	END {
		for ( n in newest ) {
			if ( ! ( n in remote ) ) print "create " newest[ n ] " " n
			else if ( ! ( ( n , remote[ n ] ) in numbered ) ) print "move " newest[ n ] " " n
		}
	}
' <( git log master --format='%H %s' ) <( printf '%s\n' "$RemoteTags" ) | sort -k3 -n )

# A worded commit is not numbered, so the plan cannot know its tag: add it.
if $Committed && ! is_int "$Message"; then
	TagPlan=$( printf '%s\ncreate %s %s' "$TagPlan" "$( git rev-parse HEAD )" "$Next" )
fi

Refspecs=()
while read -r action sha number; do
	[ -n "$action" ] || continue
	git tag -f "v1.0.${number}" "$sha" > /dev/null
	Refspecs+=( "+${sha}:refs/tags/v1.0.${number}" )
	if [ "$action" = "move" ]; then
		echo "Repaired v1.0.${number}: moved to $( git log -1 --format='%h "%s"' "$sha" )"
	else
		echo "Tagged   v1.0.${number} on $( git log -1 --format='%h "%s"' "$sha" )"
	fi
done <<< "$TagPlan"

if [ ${#Refspecs[@]} -gt 0 ]; then
	git push -q origin "${Refspecs[@]}" || exit 1
fi
echo "Pushed master ($( git log -1 --format='%h "%s"' ))."
