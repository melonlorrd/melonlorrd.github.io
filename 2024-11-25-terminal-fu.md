# Linux Tools that Improved my Terminal Fu

These Linux tools have significantly enhanced my terminal skills.

## fuser

If you're a developer, you've likely encountered issues like "port already in use" or "bind address already in use," where you have no idea which program (or process) is occupying that port. `fuser` is a handy tool that identifies processes using files or sockets. For example, let's find process (in this case, a tcp server) running at port 5000, and kill that process.

```bash
snwzd@kepler ~ > fuser 5000/tcp
5000/tcp:       163293
snwzd@kepler ~ > kill 163293
```

## objdump

I use `objdump` to list the shared libraries required by a binary. This is particularly useful when building minimal Docker images for Go applications which compile to binaries. For example:

```bash
snwzd@kepler ~ > objdump -p bin/csp | grep NEEDED
NEEDED               libresolv.so.2
NEEDED               libc.so.6
```

## find

`find` is used for searching files and directories based on a variety of conditions like name, size, modification date, and more. For example:

```bash
find . -type f -name "*.txt" -size +1M -maxdepth 3 -mindepth 2
```

Here, `.` represents the current directory. `-type f` filters only regular files, `-name "*.txt"` selects files ending in `.txt`, `-size +1M` filters by size (>1MB), `-maxdepth 3` and `-mindepth 2` restricts search depth.

## grep

`grep` is used to list files that match regex patterns.

```bash
# display lines containing the words "app", "apple", or "apples", i.e. display filename of files whose text match pattern
grep -l "^app(le|les)?$" *.txt

# display lines not containing the word "banana", i.e. display text inverse of match pattern (case insensitive)
grep -vi "^banana$" example.txt

# print lines that match with regex
grep -E "^[a-zA-Z0-9._]+@[a-zA-Z0-9]+.[a-z]{2,3}(.[a-z]{2,3})?$" email_list.txt
```

## sed

Stream editor which is commonly used to perform find and replace.

```bash
sed 's/hello/hi/n' example.txt # Replace the nth occurrence
sed 's/hello/hi/g' example.txt # Replace all occurrences
sed 's/hello/hi/ng' example.txt # Replace every nth occurrence
sed -i 's/hello/hi/g' example.txt # In-place editing, modifies the file
sed '2d' example.txt # Delete the second line
sed '/hello/d' example.txt # Delete lines containing the pattern
```

## xargs

Used to build and execute commands from standard input.

```bash
# using end of line as delimiters
echo -e "item1\nitem2\nitem3" | xargs -d '\n' -n 1 echo

# don't run if empty
echo "" | xargs -r echo {}

# replace string
ls *.md | xargs -I {} head -n 1 {} # prints first line of each .md file in current directory
```

Parallel execution:

```bash
ls *.md | xargs -P 4 -n 1 grep "example"
```

Here, -P 4 specifies 4 process can be run parallely and -n 1 makes sure one filename is passed to one grep command. I am using grep in example as grep is single threaded.

## tee

Used to redirect the output of a command to both the stdout and one or more files simultaneously. It is handy for creating verbose logs while executing scripts.

```bash
ls -l | tee file_list.txt # redirect output to file
ls -l | tee -a file_list.txt # append output to file
ls -l | tee file_list.txt backup_file_list.txt # redirecting output to multiple files
```

## tar

Used for archiving files and directories.

```bash
# creating archive, c: create v: verbose :z compression :f archive file
tar -cvzf archive.tar file1.txt file2.txt directory/

# viewing archive
tar -tvzf archive.tar

# appending files to archive
tar -rvzf archive.tar newfile.txt

# extracting archive
tar -xvzf archive.tar
```

Thankyou for reading my blog.
